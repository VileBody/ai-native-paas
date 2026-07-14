// Package mcp implements the project-scoped MCP v2 HTTP façade.
package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	kernelv2 "github.com/keir-research/ai-native-paas/contracts/kernel/v2"
	"github.com/keir-research/ai-native-paas/internal/agent/enrollment"
	"github.com/keir-research/ai-native-paas/internal/source/domain"
	agentv2 "github.com/keir-research/ai-native-paas/pkg/contracts/agent/v2"
)

type ProjectReader interface {
	GetProject(context.Context, string, string) (domain.Project, error)
	GetRepositoryForProject(context.Context, string, string) (domain.Repository, error)
}

type Handler struct {
	Enrollment   *enrollment.Service
	Projects     ProjectReader
	MaxBodyBytes int64
}

func (h Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	projectID, action, ok := route(r.URL.Path)
	if !ok {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "route not found")
		return
	}
	claims, ok := h.authenticate(r, projectID)
	if !ok {
		writeError(w, http.StatusUnauthorized, "UNAUTHENTICATED", "valid project access credential is required")
		return
	}
	switch {
	case action == "tools" && r.Method == http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]any{"api_version": agentv2.APIVersion, "semantics_version": agentv2.SemanticsVersion, "tools": agentv2.ToolCatalog()})
	case action == "invoke" && r.Method == http.MethodPost:
		h.invoke(w, r, claims)
	default:
		writeError(w, http.StatusNotFound, "NOT_FOUND", "route not found")
	}
}

func (h Handler) authenticate(r *http.Request, projectID string) (enrollment.AccessClaims, bool) {
	if h.Enrollment == nil {
		return enrollment.AccessClaims{}, false
	}
	header := strings.TrimSpace(r.Header.Get("Authorization"))
	if !strings.HasPrefix(header, "Bearer ") {
		return enrollment.AccessClaims{}, false
	}
	claims, err := h.Enrollment.AuthenticateAccess(strings.TrimSpace(strings.TrimPrefix(header, "Bearer ")), projectID)
	return claims, err == nil
}

func (h Handler) invoke(w http.ResponseWriter, r *http.Request, claims enrollment.AccessClaims) {
	if h.Projects == nil {
		writeError(w, http.StatusServiceUnavailable, "UNAVAILABLE", "project MCP dependencies are unavailable")
		return
	}
	var request agentv2.InvocationRequest
	if err := decode(r, h.limit(), &request); err != nil || request.Validate() != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid MCP v2 invocation")
		return
	}
	granted := make(map[string]struct{}, len(claims.Scopes))
	for _, scope := range claims.Scopes {
		granted[scope] = struct{}{}
	}
	verified := agentv2.VerifiedInvocationContext{
		TenantID: claims.TenantID, ProjectID: claims.ProjectID, UserID: claims.UserID,
		AgentID: claims.AgentID, GrantedScopes: granted, CredentialID: claims.CredentialID,
	}
	if err := verified.Authorize(request.Tool); err != nil {
		writeError(w, http.StatusForbidden, "FORBIDDEN", "tool scope is not granted")
		return
	}
	var (
		result any
		err    error
	)
	switch request.Tool {
	case agentv2.ToolProjectGet:
		result, err = h.Projects.GetProject(r.Context(), verified.TenantID, verified.ProjectID)
	case agentv2.ToolRepositoryStatus:
		result, err = h.Projects.GetRepositoryForProject(r.Context(), verified.TenantID, verified.ProjectID)
	default:
		writeResponse(w, http.StatusNotImplemented, agentv2.InvocationResponse{
			APIVersion: agentv2.APIVersion, InvocationID: "read-" + request.CorrelationID,
			Error: &kernelv2.PublicError{Code: "NOT_IMPLEMENTED", Message: "tool execution adapter is not available in this beta slice", Retryable: false},
		})
		return
	}
	if err != nil {
		writeResponse(w, http.StatusServiceUnavailable, agentv2.InvocationResponse{
			APIVersion: agentv2.APIVersion, InvocationID: "read-" + request.CorrelationID,
			Error: &kernelv2.PublicError{Code: "UNAVAILABLE", Message: "project read failed", Retryable: true},
		})
		return
	}
	raw, err := json.Marshal(result)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL", "response encoding failed")
		return
	}
	writeResponse(w, http.StatusOK, agentv2.InvocationResponse{APIVersion: agentv2.APIVersion, InvocationID: "read-" + request.CorrelationID, Result: raw})
}

func route(value string) (string, string, bool) {
	parts := strings.Split(strings.Trim(value, "/"), "/")
	if len(parts) != 5 || parts[0] != "projects" || parts[2] != "mcp" || parts[3] != "v2" || (parts[4] != "tools" && parts[4] != "invoke") || parts[1] == "" {
		return "", "", false
	}
	return parts[1], parts[4], true
}

func (h Handler) limit() int64 {
	if h.MaxBodyBytes <= 0 || h.MaxBodyBytes > 1<<20 {
		return 1 << 20
	}
	return h.MaxBodyBytes
}

func decode(r *http.Request, limit int64, out any) error {
	raw, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
	if err != nil || len(raw) == 0 || int64(len(raw)) > limit {
		return errors.New("invalid request body")
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

func writeResponse(w http.ResponseWriter, status int, response agentv2.InvocationResponse) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(response)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": message}})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
