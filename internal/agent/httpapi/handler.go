package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/keir-research/ai-native-paas/internal/agent/application"
	"github.com/keir-research/ai-native-paas/internal/agent/domain"
	agentv1 "github.com/keir-research/ai-native-paas/pkg/contracts/agent/v1"
)

type Handler struct {
	Agent        *application.Service
	MaxBodyBytes int64
}

type approvalDecision struct {
	ApproverUserID string `json:"approver_user_id,omitempty"`
}

type identity struct {
	TenantID    string
	PrincipalID string
	Kind        string
}

func (h Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if r.Method == http.MethodGet && r.URL.Path == "/healthz" {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
		return
	}
	if r.Method == http.MethodGet && r.URL.Path == "/mcp/v1/tools" {
		writeJSON(w, http.StatusOK, map[string]any{"api_version": agentv1.APIVersion, "semantics_version": agentv1.SemanticsVersion, "tools": agentv1.ToolCatalog()})
		return
	}
	if h.Agent == nil {
		writeDomainError(w, domain.NewError(domain.CodeUnavailable, "agent service unavailable"))
		return
	}
	if r.Method == http.MethodPost && r.URL.Path == "/mcp/v1/invoke" {
		h.invoke(w, r)
		return
	}
	parts := splitPath(r.URL.Path)
	switch {
	case len(parts) == 4 && parts[0] == "v1" && parts[1] == "tasks" && parts[3] == "status" && r.Method == http.MethodGet:
		h.getTask(w, r, parts[2])
	case len(parts) == 4 && parts[0] == "v1" && parts[1] == "tasks" && parts[3] == "audit" && r.Method == http.MethodGet:
		h.getAudit(w, r, parts[2])
	case len(parts) == 4 && parts[0] == "v1" && parts[1] == "tasks" && parts[3] == "resume" && r.Method == http.MethodPost:
		h.resume(w, r, parts[2])
	case len(parts) == 4 && parts[0] == "v1" && parts[1] == "approval-requests" && parts[3] == "grant" && r.Method == http.MethodPost:
		h.grant(w, r, parts[2])
	case len(parts) == 4 && parts[0] == "v1" && parts[1] == "approval-requests" && parts[3] == "deny" && r.Method == http.MethodPost:
		h.deny(w, r, parts[2])
	default:
		writeDomainError(w, domain.NewError(domain.CodeNotFound, "route not found"))
	}
}

func (h Handler) invoke(w http.ResponseWriter, r *http.Request) {
	id, err := authenticatedIdentity(r, "agent")
	if err != nil {
		writeDomainError(w, err)
		return
	}
	var req agentv1.InvocationRequest
	if err := decode(r, h.limit(), &req); err != nil {
		writeDomainError(w, domain.Wrap(domain.CodeInvalidArgument, "invalid MCP request", err))
		return
	}
	if req.TenantID != id.TenantID || req.AgentID != id.PrincipalID {
		writeDomainError(w, domain.NewError(domain.CodePermissionDenied, "request identity does not match authenticated principal"))
		return
	}
	if task := strings.TrimSpace(r.Header.Get("X-Task-ID")); task != "" && task != req.TaskID {
		writeDomainError(w, domain.NewError(domain.CodePermissionDenied, "task identity does not match authenticated context"))
		return
	}
	response, invokeErr := h.Agent.Invoke(r.Context(), req)
	status := http.StatusOK
	if response.Operation != nil {
		status = http.StatusAccepted
	}
	if invokeErr != nil {
		status = statusFor(invokeErr)
	}
	writeJSON(w, status, response)
}

func (h Handler) grant(w http.ResponseWriter, r *http.Request, requestID string) {
	id, err := authenticatedIdentity(r, "user", "operator")
	if err != nil {
		writeDomainError(w, err)
		return
	}
	var body approvalDecision
	if err := decodeOptional(r, h.limit(), &body); err != nil {
		writeDomainError(w, domain.Wrap(domain.CodeInvalidArgument, "invalid approval decision", err))
		return
	}
	if body.ApproverUserID != "" && body.ApproverUserID != id.PrincipalID {
		writeDomainError(w, domain.NewError(domain.CodePermissionDenied, "approval actor does not match authenticated principal"))
		return
	}
	grant, err := h.Agent.GrantApprovalForTenant(r.Context(), id.TenantID, requestID, id.PrincipalID, id.Kind)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, agentv1.ApprovalGrantView{ApprovalGrantID: grant.ID, ApprovalRequestID: grant.RequestID, Action: grant.Action, Resource: grant.Resource, PayloadHash: grant.PayloadHash, ExpiresAt: grant.ExpiresAt})
}

func (h Handler) deny(w http.ResponseWriter, r *http.Request, requestID string) {
	id, err := authenticatedIdentity(r, "user", "operator")
	if err != nil {
		writeDomainError(w, err)
		return
	}
	var body approvalDecision
	if err := decodeOptional(r, h.limit(), &body); err != nil {
		writeDomainError(w, domain.Wrap(domain.CodeInvalidArgument, "invalid approval decision", err))
		return
	}
	if body.ApproverUserID != "" && body.ApproverUserID != id.PrincipalID {
		writeDomainError(w, domain.NewError(domain.CodePermissionDenied, "approval actor does not match authenticated principal"))
		return
	}
	if err := h.Agent.DenyApprovalForTenant(r.Context(), id.TenantID, requestID, id.PrincipalID, id.Kind); err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "DENIED"})
}

func (h Handler) getTask(w http.ResponseWriter, r *http.Request, taskID string) {
	id, err := authenticatedIdentity(r, "agent", "user", "operator")
	if err != nil {
		writeDomainError(w, err)
		return
	}
	view, err := h.Agent.GetTask(r.Context(), id.TenantID, taskID)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	if id.Kind == "agent" && view.AgentID != id.PrincipalID {
		writeDomainError(w, domain.NewError(domain.CodePermissionDenied, "task denied"))
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func (h Handler) getAudit(w http.ResponseWriter, r *http.Request, taskID string) {
	id, err := authenticatedIdentity(r, "agent", "user", "operator")
	if err != nil {
		writeDomainError(w, err)
		return
	}
	view, err := h.Agent.GetTask(r.Context(), id.TenantID, taskID)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	if id.Kind == "agent" && view.AgentID != id.PrincipalID {
		writeDomainError(w, domain.NewError(domain.CodePermissionDenied, "task denied"))
		return
	}
	audit, err := h.Agent.AuditTrail(r.Context(), id.TenantID, taskID)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, audit)
}

func (h Handler) resume(w http.ResponseWriter, r *http.Request, taskID string) {
	id, err := authenticatedIdentity(r, "user", "operator")
	if err != nil {
		writeDomainError(w, err)
		return
	}
	if err := requireEmptyBody(r, h.limit()); err != nil {
		writeDomainError(w, domain.Wrap(domain.CodeInvalidArgument, "invalid request body", err))
		return
	}
	if err := h.Agent.ResumeTask(r.Context(), id.TenantID, taskID, id.PrincipalID); err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ACTIVE"})
}

func authenticatedIdentity(r *http.Request, allowed ...string) (identity, error) {
	id := identity{TenantID: strings.TrimSpace(r.Header.Get("X-Tenant-ID")), PrincipalID: strings.TrimSpace(r.Header.Get("X-Principal-ID")), Kind: strings.ToLower(strings.TrimSpace(r.Header.Get("X-Principal-Kind")))}
	if !agentv1.ValidID(id.TenantID) || !agentv1.ValidID(id.PrincipalID) || id.Kind == "" {
		return identity{}, domain.NewError(domain.CodePermissionDenied, "authenticated principal headers required")
	}
	for _, value := range allowed {
		if id.Kind == value {
			return id, nil
		}
	}
	return identity{}, domain.NewError(domain.CodePermissionDenied, "principal kind denied")
}

func (h Handler) limit() int64 {
	if h.MaxBodyBytes <= 0 {
		return 1 << 20
	}
	return h.MaxBodyBytes
}

func decode(r *http.Request, limit int64, out any) error {
	raw, err := readLimited(r, limit)
	if err != nil {
		return err
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return errors.New("request body is required")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		return err
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}
func decodeOptional(r *http.Request, limit int64, out any) error {
	raw, err := readLimited(r, limit)
	if err != nil {
		return err
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		return err
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}
func requireEmptyBody(r *http.Request, limit int64) error {
	raw, err := readLimited(r, limit)
	if err != nil {
		return err
	}
	trimmed := strings.TrimSpace(string(raw))
	if trimmed != "" && trimmed != "{}" {
		return errors.New("body must be empty")
	}
	return nil
}
func readLimited(r *http.Request, limit int64) ([]byte, error) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > limit {
		return nil, errors.New("request body too large")
	}
	return raw, nil
}
func splitPath(value string) []string {
	trimmed := strings.Trim(value, "/")
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, "/")
}
func statusFor(err error) int {
	switch domain.AsError(err).Code {
	case domain.CodeInvalidArgument:
		return http.StatusBadRequest
	case domain.CodePermissionDenied:
		return http.StatusForbidden
	case domain.CodeNotFound:
		return http.StatusNotFound
	case domain.CodeConflict, domain.CodeApprovalRequired, domain.CodePaused, domain.CodeBudgetExceeded, domain.CodeEntitlementDenied:
		return http.StatusConflict
	case domain.CodeCanceled:
		return http.StatusGone
	default:
		return http.StatusServiceUnavailable
	}
}
func writeDomainError(w http.ResponseWriter, err error) {
	d := domain.AsError(err)
	writeJSON(w, statusFor(err), map[string]any{"error": map[string]any{"code": d.Code, "message": safeTransportMessage(d), "retryable": d.Retryable}})
}
func safeTransportMessage(err *domain.Error) string {
	switch err.Code {
	case domain.CodeInvalidArgument, domain.CodePermissionDenied, domain.CodeNotFound, domain.CodeConflict, domain.CodeBudgetExceeded, domain.CodeApprovalRequired, domain.CodePaused, domain.CodeEntitlementDenied, domain.CodeCanceled:
		return err.Message
	default:
		return "platform operation is temporarily unavailable"
	}
}
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
