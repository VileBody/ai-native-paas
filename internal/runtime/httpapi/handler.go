package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/keir-research/ai-native-paas/internal/runtime/application"
	"github.com/keir-research/ai-native-paas/internal/runtime/domain"
	buildv1 "github.com/keir-research/ai-native-paas/pkg/contracts/build/v1"
	runtimev1 "github.com/keir-research/ai-native-paas/pkg/contracts/runtime/v1"
)

type Handler struct {
	Runtime      *application.Service
	MaxBodyBytes int64
}

func (h Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method == http.MethodGet && r.URL.Path == "/healthz" {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
		return
	}
	parts := splitPath(r.URL.Path)
	if len(parts) < 3 || parts[0] != "v1" || parts[1] != "organizations" {
		writeError(w, domain.NewError(domain.CodeNotFound, "route not found"))
		return
	}
	tenantID := parts[2]
	actorID, err := authorize(r, tenantID)
	if err != nil {
		writeError(w, err)
		return
	}
	if h.Runtime == nil {
		writeError(w, domain.NewError(domain.CodeUnavailable, "runtime service unavailable"))
		return
	}

	switch {
	case len(parts) == 4 && parts[3] == "applications" && r.Method == http.MethodPost:
		h.createApplication(w, r, tenantID, actorID)
	case len(parts) == 6 && parts[3] == "applications" && parts[5] == "environments" && r.Method == http.MethodPost:
		h.createEnvironment(w, r, tenantID, actorID, parts[4])
	case len(parts) == 8 && parts[3] == "applications" && parts[5] == "environments" && parts[7] == "deployments" && r.Method == http.MethodPost:
		h.deploy(w, r, tenantID, actorID, parts[4], parts[6])
	case len(parts) == 5 && parts[3] == "deployments" && r.Method == http.MethodGet:
		h.status(w, r, tenantID, parts[4])
	case len(parts) == 6 && parts[3] == "environments" && parts[5] == "rollbacks" && r.Method == http.MethodPost:
		h.rollback(w, r, tenantID, actorID, parts[4])
	case len(parts) == 6 && parts[3] == "deployments" && parts[5] == "rollback" && r.Method == http.MethodPost:
		h.rollbackDeployment(w, r, tenantID, actorID, parts[4])
	default:
		writeError(w, domain.NewError(domain.CodeNotFound, "route not found"))
	}
}

type createApplicationBody struct {
	ProjectID string `json:"project_id"`
	Name      string `json:"name"`
}

func (h Handler) createApplication(w http.ResponseWriter, r *http.Request, tenantID, actorID string) {
	var body createApplicationBody
	if err := decode(r, h.limit(), &body); err != nil {
		writeError(w, domain.Wrap(domain.CodeInvalidArgument, "invalid json", err))
		return
	}
	app, env, err := h.Runtime.CreateApplication(r.Context(), application.CreateApplicationRequest{
		TenantID: tenantID, ProjectID: body.ProjectID, Name: body.Name, ActorID: actorID,
		IdempotencyKey: strings.TrimSpace(r.Header.Get("Idempotency-Key")),
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"application": app, "default_environment": env})
}

type createEnvironmentBody struct {
	Name    string `json:"name"`
	Default bool   `json:"default"`
}

func (h Handler) createEnvironment(w http.ResponseWriter, r *http.Request, tenantID, actorID, applicationID string) {
	var body createEnvironmentBody
	if err := decode(r, h.limit(), &body); err != nil {
		writeError(w, domain.Wrap(domain.CodeInvalidArgument, "invalid json", err))
		return
	}
	env, err := h.Runtime.CreateEnvironment(r.Context(), application.CreateEnvironmentRequest{
		TenantID: tenantID, ApplicationID: applicationID, Name: body.Name, Default: body.Default,
		ActorID: actorID, IdempotencyKey: strings.TrimSpace(r.Header.Get("Idempotency-Key")),
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, env)
}

type deployBody struct {
	Artifact      buildv1.ArtifactRef     `json:"artifact"`
	Configuration runtimev1.ReleaseConfig `json:"configuration"`
}

func (h Handler) deploy(w http.ResponseWriter, r *http.Request, tenantID, actorID, applicationID, environmentID string) {
	var body deployBody
	if err := decode(r, h.limit(), &body); err != nil {
		writeError(w, domain.Wrap(domain.CodeInvalidArgument, "invalid json", err))
		return
	}
	result, err := h.Runtime.Deploy(r.Context(), runtimev1.DeployRequest{
		TenantID: tenantID, ApplicationID: applicationID, EnvironmentID: environmentID,
		Artifact: body.Artifact, Configuration: body.Configuration,
		IdempotencyKey: strings.TrimSpace(r.Header.Get("Idempotency-Key")), ActorID: actorID,
		ExpectedEnvironmentRevision: expectedEnvironmentRevision(r),
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, result)
}

func (h Handler) status(w http.ResponseWriter, r *http.Request, tenantID, deploymentID string) {
	result, err := h.Runtime.StatusForTenant(r.Context(), tenantID, deploymentID)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

type rollbackBody struct {
	TargetReleaseID  string `json:"target_release_id"`
	CriticalOverride bool   `json:"critical_override"`
}

func (h Handler) rollback(w http.ResponseWriter, r *http.Request, tenantID, actorID, environmentID string) {
	var body rollbackBody
	if err := decode(r, h.limit(), &body); err != nil {
		writeError(w, domain.Wrap(domain.CodeInvalidArgument, "invalid json", err))
		return
	}
	result, err := h.Runtime.RollbackWithRequest(r.Context(), application.RollbackRequest{
		TenantID: tenantID, EnvironmentID: environmentID, TargetReleaseID: body.TargetReleaseID,
		ActorID: actorID, IdempotencyKey: strings.TrimSpace(r.Header.Get("Idempotency-Key")), CriticalOverride: body.CriticalOverride,
		ExpectedEnvironmentRevision: expectedEnvironmentRevision(r),
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, result)
}

func (h Handler) rollbackDeployment(w http.ResponseWriter, r *http.Request, tenantID, actorID, deploymentID string) {
	var body rollbackBody
	if err := decode(r, h.limit(), &body); err != nil {
		writeError(w, domain.Wrap(domain.CodeInvalidArgument, "invalid json", err))
		return
	}
	result, err := h.Runtime.RollbackDeploymentWithRequest(r.Context(), deploymentID, application.RollbackRequest{
		TenantID: tenantID, TargetReleaseID: body.TargetReleaseID, ActorID: actorID,
		IdempotencyKey: strings.TrimSpace(r.Header.Get("Idempotency-Key")), CriticalOverride: body.CriticalOverride,
		ExpectedEnvironmentRevision: expectedEnvironmentRevision(r),
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, result)
}

func expectedEnvironmentRevision(r *http.Request) int64 {
	raw := strings.TrimSpace(r.Header.Get("X-Expected-Environment-Revision"))
	if raw == "" {
		return 0
	}
	revision, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || revision < 1 {
		return -1
	}
	return revision
}

func (h Handler) limit() int64 {
	if h.MaxBodyBytes <= 0 {
		return 2 << 20
	}
	return h.MaxBodyBytes
}

func authorize(r *http.Request, pathTenant string) (string, error) {
	actor := strings.TrimSpace(r.Header.Get("X-Principal-ID"))
	tenant := strings.TrimSpace(r.Header.Get("X-Tenant-ID"))
	if actor == "" || tenant == "" {
		return "", domain.NewError(domain.CodeForbidden, "principal headers required")
	}
	if tenant != pathTenant {
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
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
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
	code := domain.CodeInternal
	message := "request failed"
	var typed *domain.Error
	if errors.As(err, &typed) {
		code, message = typed.Code, typed.Message
		switch typed.Code {
		case domain.CodeInvalidArgument:
			status = http.StatusBadRequest
		case domain.CodeForbidden:
			status = http.StatusForbidden
		case domain.CodeNotFound:
			status = http.StatusNotFound
		case domain.CodeConflict, domain.CodeStaleVersion, domain.CodePolicyRejected, domain.CodeCapacity:
			status = http.StatusConflict
		case domain.CodeUnavailable, domain.CodeInternal:
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
