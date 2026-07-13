package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/keir-research/ai-native-paas/internal/build/application"
	"github.com/keir-research/ai-native-paas/internal/build/domain"
	sourcev1 "github.com/keir-research/ai-native-paas/pkg/contracts/source/v1"
)

type Handler struct {
	Build        *application.Service
	MaxBodyBytes int64
}

func (h Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method == http.MethodGet && r.URL.Path == "/healthz" {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
		return
	}
	parts := splitPath(r.URL.Path)
	if len(parts) < 4 || parts[0] != "v1" || parts[1] != "organizations" || parts[3] != "builds" {
		writeError(w, domain.NewError(domain.CodeNotFound, "route not found"))
		return
	}
	tenantID := parts[2]
	actorID, err := authorize(r, tenantID)
	if err != nil {
		writeError(w, err)
		return
	}
	if h.Build == nil {
		writeError(w, domain.NewError(domain.CodeUnavailable, "build service unavailable"))
		return
	}
	switch {
	case len(parts) == 4 && r.Method == http.MethodPost:
		h.requestBuild(w, r, tenantID, actorID)
	case len(parts) == 5 && r.Method == http.MethodGet:
		h.getBuild(w, r, tenantID, parts[4])
	case len(parts) == 6 && parts[5] == "run" && r.Method == http.MethodPost:
		h.runBuild(w, r, tenantID, actorID, parts[4])
	case len(parts) == 6 && parts[5] == "cancel" && r.Method == http.MethodPost:
		h.cancelBuild(w, r, tenantID, parts[4])
	case len(parts) == 6 && parts[5] == "retry" && r.Method == http.MethodPost:
		h.retryBuild(w, r, tenantID, actorID, parts[4])
	case len(parts) == 6 && parts[5] == "logs" && r.Method == http.MethodGet:
		h.logs(w, r, tenantID, parts[4])
	default:
		writeError(w, domain.NewError(domain.CodeNotFound, "route not found"))
	}
}

type requestBuildBody struct {
	Source          sourcev1.SourceRevision `json:"source"`
	Config          domain.BuildConfig      `json:"config"`
	BuilderDigest   string                  `json:"builder_digest"`
	RunImageDigest  string                  `json:"run_image_digest"`
	PlatformVersion string                  `json:"platform_version"`
}

func (h Handler) requestBuild(w http.ResponseWriter, r *http.Request, tenantID, actorID string) {
	var body requestBuildBody
	if err := decode(r, h.limit(), &body); err != nil {
		writeError(w, domain.Wrap(domain.CodeInvalidArgument, "invalid json", err))
		return
	}
	result, err := h.Build.RequestBuild(r.Context(), application.RequestBuildCommand{
		TenantID: tenantID, ActorID: actorID, CorrelationID: correlationID(r), IdempotencyKey: strings.TrimSpace(r.Header.Get("Idempotency-Key")),
		Source: body.Source, Config: body.Config, BuilderDigest: body.BuilderDigest, RunImageDigest: body.RunImageDigest, PlatformVersion: body.PlatformVersion,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, result)
}
func (h Handler) getBuild(w http.ResponseWriter, r *http.Request, tenantID, buildID string) {
	build, artifact, err := h.Build.GetBuild(r.Context(), tenantID, buildID)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, application.RequestBuildResult{Build: build, Artifact: artifact, Reused: build.ArtifactID != ""})
}
func (h Handler) runBuild(w http.ResponseWriter, r *http.Request, tenantID, actorID, buildID string) {
	build, artifact, err := h.Build.RunBuild(r.Context(), tenantID, actorID, buildID)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, application.RequestBuildResult{Build: build, Artifact: artifact})
}
func (h Handler) cancelBuild(w http.ResponseWriter, r *http.Request, tenantID, buildID string) {
	build, err := h.Build.CancelBuild(r.Context(), tenantID, buildID)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, build)
}
func (h Handler) retryBuild(w http.ResponseWriter, r *http.Request, tenantID, actorID, buildID string) {
	result, err := h.Build.RetryBuild(r.Context(), application.RetryBuildCommand{TenantID: tenantID, ActorID: actorID, CorrelationID: correlationID(r), IdempotencyKey: strings.TrimSpace(r.Header.Get("Idempotency-Key")), BuildID: buildID})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, result)
}
func (h Handler) logs(w http.ResponseWriter, r *http.Request, tenantID, buildID string) {
	raw, err := h.Build.StreamBuildLogs(r.Context(), tenantID, buildID)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"build_id": buildID, "logs": string(raw)})
}
func (h Handler) limit() int64 {
	if h.MaxBodyBytes <= 0 {
		return 2 << 20
	}
	return h.MaxBodyBytes
}
func correlationID(r *http.Request) string {
	value := strings.TrimSpace(r.Header.Get("X-Correlation-ID"))
	if value == "" {
		value = strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	}
	return value
}
func authorize(r *http.Request, pathTenant string) (string, error) {
	actor := strings.TrimSpace(r.Header.Get("X-Principal-ID"))
	claimTenant := strings.TrimSpace(r.Header.Get("X-Tenant-ID"))
	if actor == "" || claimTenant == "" {
		return "", domain.NewError(domain.CodeForbidden, "principal headers required")
	}
	if claimTenant != pathTenant {
		return "", domain.NewError(domain.CodeForbidden, "tenant path does not match principal")
	}
	return actor, nil
}
func decode(r *http.Request, limit int64, out any) error {
	if limit <= 0 {
		return errors.New("invalid body limit")
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
	if err != nil {
		return err
	}
	if int64(len(raw)) > limit {
		return errors.New("request body exceeds limit")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		return err
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple json values")
		}
		return err
	}
	return nil
}
func splitPath(value string) []string {
	raw := strings.Split(strings.Trim(value, "/"), "/")
	out := raw[:0]
	for _, item := range raw {
		if item != "" {
			out = append(out, item)
		}
	}
	return out
}

type publicError struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func writeError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	code := domain.CodeUnavailable
	message := "request failed"
	var typed *domain.Error
	if errors.As(err, &typed) {
		code, message = typed.Code, typed.Message
		switch typed.Code {
		case domain.CodeInvalidArgument, domain.CodeUserFailure:
			status = http.StatusBadRequest
		case domain.CodeForbidden:
			status = http.StatusForbidden
		case domain.CodeNotFound:
			status = http.StatusNotFound
		case domain.CodeConflict, domain.CodeStaleVersion, domain.CodePolicyRejected:
			status = http.StatusConflict
		case domain.CodeUnavailable, domain.CodePlatformFailure, domain.CodeTimeout:
			status = http.StatusServiceUnavailable
		}
	}
	var payload publicError
	payload.Error.Code = string(code)
	payload.Error.Message = message
	writeJSON(w, status, payload)
}
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
