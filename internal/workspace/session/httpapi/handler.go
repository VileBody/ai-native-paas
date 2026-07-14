// Package httpapi exposes the workspace agent's outbound mTLS protocol.
package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/keir-research/ai-native-paas/internal/workspace"
	"github.com/keir-research/ai-native-paas/internal/workspace/session"
	workspacev1 "github.com/keir-research/ai-native-paas/pkg/contracts/workspace/v1"
)

type Handler struct {
	Registry     *session.Registry
	Workspaces   *workspace.Service
	Bindings     AgentBindingResolver
	Principals   PrincipalResolver
	MaxBodyBytes int64
	LongPoll     time.Duration
}

type AgentBindingResolver interface {
	ResolveAgentBinding(context.Context, string, string, string, string, string) (string, error)
}

func (h Handler) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Content-Type", "application/json")
	response.Header().Set("Cache-Control", "no-store")
	response.Header().Set("X-Content-Type-Options", "nosniff")
	switch {
	case request.Method == http.MethodPost && request.URL.Path == "/api/v1/workspace-agent/session":
		h.connect(response, request)
	case request.Method == http.MethodPost && request.URL.Path == "/api/v1/workspace-agent/session:heartbeat":
		h.heartbeat(response, request)
	case request.Method == http.MethodGet && request.URL.Path == "/api/v1/workspace-agent/messages:next":
		h.next(response, request)
	case request.Method == http.MethodPost && strings.HasPrefix(request.URL.Path, "/api/v1/workspace-agent/messages/") && strings.HasSuffix(request.URL.Path, ":ack"):
		h.acknowledge(response, request)
	case request.Method == http.MethodPost && request.URL.Path == "/api/v1/workspace-agent/outcomes":
		h.outcome(response, request)
	default:
		writeError(response, http.StatusNotFound, "NOT_FOUND", "route not found")
	}
}

func (h Handler) connect(response http.ResponseWriter, request *http.Request) {
	principal, ok := h.authenticate(response, request)
	if !ok {
		return
	}
	var body workspacev1.AgentSessionConnect
	if decode(request, h.bodyLimit(), &body) != nil {
		writeError(response, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid workspace session request")
		return
	}
	if h.Bindings == nil || strings.TrimSpace(body.CorrelationID) == "" {
		writeError(response, http.StatusUnauthorized, "UNAUTHENTICATED", "workspace provider binding is required")
		return
	}
	vmID, err := h.Bindings.ResolveAgentBinding(request.Context(), principal.TenantID, principal.ProjectID, principal.WorkspaceID, principal.TaskID, body.CorrelationID)
	if err != nil {
		writeError(response, http.StatusUnauthorized, "UNAUTHENTICATED", "workspace provider binding is invalid")
		return
	}
	view, err := h.Registry.Connect(request.Context(), principal, vmID)
	if err != nil {
		writeSessionError(response, err)
		return
	}
	writeJSON(response, http.StatusCreated, view)
}

func (h Handler) heartbeat(response http.ResponseWriter, request *http.Request) {
	principal, ok := h.authenticate(response, request)
	if !ok {
		return
	}
	var body workspacev1.AgentHeartbeat
	if decode(request, h.bodyLimit(), &body) != nil {
		writeError(response, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid workspace heartbeat")
		return
	}
	if err := h.Registry.Heartbeat(request.Context(), principal, body.SessionID); err != nil {
		writeSessionError(response, err)
		return
	}
	response.WriteHeader(http.StatusNoContent)
}

func (h Handler) next(response http.ResponseWriter, request *http.Request) {
	principal, ok := h.authenticate(response, request)
	if !ok {
		return
	}
	sessionID := strings.TrimSpace(request.URL.Query().Get("session_id"))
	message, err := h.Registry.WaitNext(request.Context(), principal, sessionID, h.longPoll())
	if errors.Is(err, session.ErrNotFound) {
		response.WriteHeader(http.StatusNoContent)
		return
	}
	if err != nil {
		writeSessionError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, message)
}

func (h Handler) acknowledge(response http.ResponseWriter, request *http.Request) {
	principal, ok := h.authenticate(response, request)
	if !ok {
		return
	}
	messageID := strings.TrimSuffix(strings.TrimPrefix(request.URL.Path, "/api/v1/workspace-agent/messages/"), ":ack")
	if messageID == "" || strings.Contains(messageID, "/") {
		writeError(response, http.StatusNotFound, "NOT_FOUND", "message not found")
		return
	}
	var body workspacev1.AgentMessageAck
	if decode(request, h.bodyLimit(), &body) != nil {
		writeError(response, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid workspace acknowledgement")
		return
	}
	if err := h.Registry.Acknowledge(request.Context(), principal, body.SessionID, messageID, body.Accepted); err != nil {
		writeSessionError(response, err)
		return
	}
	response.WriteHeader(http.StatusNoContent)
}

func (h Handler) outcome(response http.ResponseWriter, request *http.Request) {
	principal, ok := h.authenticate(response, request)
	if !ok {
		return
	}
	if h.Workspaces == nil {
		writeError(response, http.StatusServiceUnavailable, "UNAVAILABLE", "workspace outcome service unavailable")
		return
	}
	var body workspacev1.AgentCommandOutcome
	if decode(request, h.bodyLimit(), &body) != nil {
		writeError(response, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid workspace outcome")
		return
	}
	bound, err := h.Registry.AuthorizeOutcome(request.Context(), principal, body.SessionID)
	if err != nil {
		writeSessionError(response, err)
		return
	}
	view, err := h.Workspaces.RecordOutcome(request.Context(), workspace.CommandOutcome{
		TenantID: bound.TenantID, ProjectID: bound.ProjectID, WorkspaceID: bound.WorkspaceID,
		CommandID: body.CommandID, AgentSessionID: bound.ID, VMID: bound.VMID, State: body.State,
		ExitCode: body.ExitCode, FinishedAt: body.FinishedAt, ProcessTreeTerminated: body.ProcessTreeTerminated,
	})
	if err != nil {
		writeError(response, http.StatusConflict, "OUTCOME_REJECTED", "workspace outcome does not match a running command")
		return
	}
	writeJSON(response, http.StatusOK, view)
}

func (h Handler) authenticate(response http.ResponseWriter, request *http.Request) (session.Principal, bool) {
	if h.Registry == nil || h.Principals == nil {
		writeError(response, http.StatusServiceUnavailable, "UNAVAILABLE", "workspace agent channel unavailable")
		return session.Principal{}, false
	}
	principal, err := h.Principals.Resolve(request)
	if err != nil {
		writeError(response, http.StatusUnauthorized, "UNAUTHENTICATED", "verified workspace client certificate required")
		return session.Principal{}, false
	}
	return principal, true
}

func (h Handler) bodyLimit() int64 {
	if h.MaxBodyBytes <= 0 || h.MaxBodyBytes > 1<<20 {
		return 64 << 10
	}
	return h.MaxBodyBytes
}

func (h Handler) longPoll() time.Duration {
	if h.LongPoll <= 0 || h.LongPoll > 25*time.Second {
		return 20 * time.Second
	}
	return h.LongPoll
}

func decode(request *http.Request, limit int64, target any) error {
	raw, err := io.ReadAll(io.LimitReader(request.Body, limit+1))
	if err != nil || len(raw) == 0 || int64(len(raw)) > limit {
		return errors.New("invalid JSON body")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("request must contain one JSON value")
	}
	return nil
}

func writeSessionError(response http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, session.ErrNotFound):
		writeError(response, http.StatusUnauthorized, "SESSION_EXPIRED", "workspace agent session is unavailable")
	case errors.Is(err, session.ErrConflict):
		writeError(response, http.StatusForbidden, "PERMISSION_DENIED", "workspace certificate is bound to another session")
	default:
		writeError(response, http.StatusServiceUnavailable, "UNAVAILABLE", "workspace agent channel failed")
	}
}

func writeError(response http.ResponseWriter, status int, code, message string) {
	writeJSON(response, status, map[string]any{"error": map[string]string{"code": code, "message": message}})
}

func writeJSON(response http.ResponseWriter, status int, value any) {
	response.WriteHeader(status)
	if status != http.StatusNoContent {
		_ = json.NewEncoder(response).Encode(value)
	}
}
