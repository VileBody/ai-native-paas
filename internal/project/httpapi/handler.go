// Package httpapi exposes the controlled-beta project management API.
package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/keir-research/ai-native-paas/internal/identity/httpauth"
	projectapp "github.com/keir-research/ai-native-paas/internal/project/application"
	"github.com/keir-research/ai-native-paas/internal/source/domain"
	projectv2 "github.com/keir-research/ai-native-paas/pkg/contracts/project/v2"
)

type Handler struct {
	Projects     *projectapp.Service
	MaxBodyBytes int64
}

func (h Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if r.Method == http.MethodGet && r.URL.Path == "/healthz" {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
		return
	}
	if r.Method == http.MethodPost && r.URL.Path == "/api/v2/projects" {
		h.create(w, r)
		return
	}
	writeError(w, http.StatusNotFound, "NOT_FOUND", "route not found")
}

func (h Handler) create(w http.ResponseWriter, r *http.Request) {
	if h.Projects == nil {
		writeError(w, http.StatusServiceUnavailable, "UNAVAILABLE", "project service unavailable")
		return
	}
	identity, ok := httpauth.IdentityFromContext(r.Context())
	if !ok || identity.KindClaim != "USER" || strings.TrimSpace(identity.TenantID) == "" || strings.TrimSpace(identity.UserID) == "" {
		writeError(w, http.StatusForbidden, "FORBIDDEN", "verified user identity is required")
		return
	}
	idempotencyKey := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	var request projectv2.CreateProjectRequest
	if err := decode(r, h.limit(), &request); err != nil || request.Validate() != nil || idempotencyKey == "" {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid project creation request")
		return
	}
	response, err := h.Projects.Create(r.Context(), projectapp.CreateCommand{
		TenantID: identity.TenantID, UserID: identity.UserID, Name: request.Name, IdempotencyKey: idempotencyKey,
	})
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, response)
}

func (h Handler) limit() int64 {
	if h.MaxBodyBytes <= 0 || h.MaxBodyBytes > 1<<20 {
		return 64 << 10
	}
	return h.MaxBodyBytes
}

func decode(r *http.Request, limit int64, out any) error {
	raw, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
	if err != nil || len(raw) == 0 || int64(len(raw)) > limit {
		return errors.New("invalid body")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("request must contain one JSON value")
	}
	return nil
}

func writeServiceError(w http.ResponseWriter, err error) {
	var sourceError *domain.Error
	if errors.As(err, &sourceError) {
		switch sourceError.Code {
		case domain.CodeInvalidArgument:
			writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", sourceError.Message)
		case domain.CodeConflict, domain.CodeStaleVersion:
			writeError(w, http.StatusConflict, "CONFLICT", sourceError.Message)
		default:
			writeError(w, http.StatusServiceUnavailable, "UNAVAILABLE", "project creation is temporarily unavailable")
		}
		return
	}
	writeError(w, http.StatusServiceUnavailable, "UNAVAILABLE", "project creation is temporarily unavailable")
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": message}})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
