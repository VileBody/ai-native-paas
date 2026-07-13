package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/keir-research/ai-native-paas/internal/source/application"
	"github.com/keir-research/ai-native-paas/internal/source/domain"
)

type Handler struct {
	Source       *application.Service
	Webhooks     *application.WebhookService
	MaxBodyBytes int64
}

func (h Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	parts := splitPath(r.URL.Path)
	if r.Method == http.MethodGet && r.URL.Path == "/healthz" {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
		return
	}
	if len(parts) == 4 && parts[0] == "v1" && parts[1] == "organizations" && parts[3] == "projects" && r.Method == http.MethodPost {
		h.createProject(w, r, parts[2])
		return
	}
	if len(parts) == 5 && parts[0] == "v1" && parts[1] == "organizations" && parts[3] == "projects" && r.Method == http.MethodGet {
		h.getProject(w, r, parts[2], parts[4])
		return
	}
	if len(parts) == 6 && parts[0] == "v1" && parts[1] == "organizations" && parts[3] == "repositories" && parts[5] == "provision" && r.Method == http.MethodPost {
		h.provision(w, r, parts[2], parts[4])
		return
	}
	if len(parts) == 3 && parts[0] == "hooks" && parts[1] == "gitlab" && r.Method == http.MethodPost {
		h.webhook(w, r, parts[2])
		return
	}
	writeError(w, domain.NewError(domain.CodeNotFound, "route not found"))
}
func (h Handler) createProject(w http.ResponseWriter, r *http.Request, tenant string) {
	actor, err := authorize(r, tenant)
	if err != nil {
		writeError(w, err)
		return
	}
	var body struct {
		Name                string `json:"name"`
		ProviderNamespaceID int64  `json:"provider_namespace_id"`
	}
	if err = decode(r, h.limit(), &body); err != nil {
		writeError(w, domain.Wrap(domain.CodeInvalidArgument, "invalid json", err))
		return
	}
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	result, err := h.Source.CreateProject(r.Context(), application.CreateProjectCommand{TenantID: tenant, ActorID: actor, Name: body.Name, ProviderNamespaceID: body.ProviderNamespaceID, IdempotencyKey: key})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, result)
}
func (h Handler) getProject(w http.ResponseWriter, r *http.Request, tenant, id string) {
	if _, err := authorize(r, tenant); err != nil {
		writeError(w, err)
		return
	}
	p, err := h.Source.GetProject(r.Context(), tenant, id)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}
func (h Handler) provision(w http.ResponseWriter, r *http.Request, tenant, repoID string) {
	actor, err := authorize(r, tenant)
	if err != nil {
		writeError(w, err)
		return
	}
	repo, err := h.Source.ProvisionRepository(r.Context(), application.ProvisionRepositoryCommand{TenantID: tenant, ActorID: actor, RepositoryID: repoID})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, repo)
}
func (h Handler) webhook(w http.ResponseWriter, r *http.Request, tenant string) {
	if h.Webhooks == nil {
		writeError(w, domain.NewError(domain.CodeUnavailable, "webhook service unavailable"))
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, h.limit()))
	if err != nil {
		writeError(w, domain.Wrap(domain.CodeInvalidArgument, "webhook body rejected", err))
		return
	}
	headers := map[string][]string(r.Header.Clone())
	result, err := h.Webhooks.Handle(r.Context(), tenant, headers, raw)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, result)
}
func (h Handler) limit() int64 {
	if h.MaxBodyBytes <= 0 {
		return 2 << 20
	}
	return h.MaxBodyBytes
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
	dec := json.NewDecoder(io.LimitReader(r.Body, limit))
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
func splitPath(v string) []string {
	raw := strings.Split(strings.Trim(v, "/"), "/")
	out := raw[:0]
	for _, p := range raw {
		if p != "" {
			out = append(out, p)
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
	var e *domain.Error
	if errors.As(err, &e) {
		code = e.Code
		message = e.Message
		switch e.Code {
		case domain.CodeInvalidArgument:
			status = http.StatusBadRequest
		case domain.CodeForbidden:
			status = http.StatusForbidden
		case domain.CodeNotFound:
			status = http.StatusNotFound
		case domain.CodeConflict, domain.CodeStaleVersion:
			status = http.StatusConflict
		case domain.CodeExternal, statusCodeUnavailable():
			status = http.StatusBadGateway
		}
	}
	var payload publicError
	payload.Error.Code = string(code)
	payload.Error.Message = message
	writeJSON(w, status, payload)
}
func statusCodeUnavailable() domain.ErrorCode { return domain.CodeUnavailable }
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
