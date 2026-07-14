// Package httpapi exposes the workspace agent's outbound mTLS protocol.
package httpapi

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/keir-research/ai-native-paas/internal/workspace"
	"github.com/keir-research/ai-native-paas/internal/workspace/bootstrap"
	"github.com/keir-research/ai-native-paas/internal/workspace/session"
	infrastructurev1 "github.com/keir-research/ai-native-paas/pkg/contracts/infrastructure/v1"
	sourcev2 "github.com/keir-research/ai-native-paas/pkg/contracts/source/v2"
	workspacev1 "github.com/keir-research/ai-native-paas/pkg/contracts/workspace/v1"
)

type Handler struct {
	Registry       *session.Registry
	Workspaces     *workspace.Service
	Credentials    WorkspaceCredentialResolver
	Outputs        WorkspaceOutputWriter
	PlanReceipts   WorkspacePlanReceiptWriter
	CommitReceipts WorkspaceCommitReceiptWriter
	Bindings       AgentBindingResolver
	Certificates   CertificateRotator
	Principals     PrincipalResolver
	MaxBodyBytes   int64
	LongPoll       time.Duration
}

type AgentBindingResolver interface {
	ResolveAgentBinding(context.Context, string, string, string, string, string) (string, error)
}

type CertificateRotator interface {
	Sign(context.Context, bootstrap.Identity, []byte) (bootstrap.CertificateBundle, error)
}

type WorkspaceCredentialResolver interface {
	ResolveCredentials(context.Context, workspace.CredentialResolveRequest) (workspacev1.AgentCredentialView, error)
}

type WorkspaceOutputWriter interface {
	RecordOutputChunk(context.Context, workspace.CredentialResolveRequest, workspacev1.AgentOutputChunk) error
}

type WorkspacePlanReceiptWriter interface {
	RecordPlanReceipt(context.Context, workspace.CredentialResolveRequest, infrastructurev1.AgentPlanReceipt) error
}

type WorkspaceCommitReceiptWriter interface {
	RecordCommitReceipt(context.Context, workspace.CredentialResolveRequest, sourcev2.AgentCommitReceipt) error
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
	case request.Method == http.MethodPost && request.URL.Path == "/api/v1/workspace-agent/certificate:rotate":
		h.rotateCertificate(response, request)
	case request.Method == http.MethodGet && request.URL.Path == "/api/v1/workspace-agent/messages:next":
		h.next(response, request)
	case request.Method == http.MethodPost && strings.HasPrefix(request.URL.Path, "/api/v1/workspace-agent/messages/") && strings.HasSuffix(request.URL.Path, ":ack"):
		h.acknowledge(response, request)
	case request.Method == http.MethodPost && request.URL.Path == "/api/v1/workspace-agent/outcomes":
		h.outcome(response, request)
	case request.Method == http.MethodPost && request.URL.Path == "/api/v1/workspace-agent/credentials:resolve":
		h.resolveCredentials(response, request)
	case request.Method == http.MethodPost && request.URL.Path == "/api/v1/workspace-agent/output-chunks":
		h.outputChunk(response, request)
	case request.Method == http.MethodPost && request.URL.Path == "/api/v1/workspace-agent/plan-receipts":
		h.planReceipt(response, request)
	case request.Method == http.MethodPost && request.URL.Path == "/api/v1/workspace-agent/commit-receipts":
		h.commitReceipt(response, request)
	default:
		writeError(response, http.StatusNotFound, "NOT_FOUND", "route not found")
	}
}

func (h Handler) commitReceipt(response http.ResponseWriter, request *http.Request) {
	principal, ok := h.authenticate(response, request)
	if !ok {
		return
	}
	if h.CommitReceipts == nil {
		writeError(response, http.StatusServiceUnavailable, "UNAVAILABLE", "workspace commit receipt service unavailable")
		return
	}
	var body sourcev2.AgentCommitReceipt
	if decode(request, 64<<10, &body) != nil || body.Validate() != nil || body.Statement.AgentID != principal.AgentID || body.Statement.TaskID != principal.TaskID || body.CertificateFingerprint != principal.CertificateID || !verifyCommitSignature(request, body) {
		writeError(response, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid workspace commit receipt")
		return
	}
	bound, err := h.Registry.AuthorizeOutcomeFor(request.Context(), principal, body.SessionID, body.ExecutionSessionID)
	if err != nil {
		writeSessionError(response, err)
		return
	}
	if err := h.CommitReceipts.RecordCommitReceipt(request.Context(), workspace.CredentialResolveRequest{
		TenantID: bound.TenantID, ProjectID: bound.ProjectID, WorkspaceID: bound.WorkspaceID, TaskID: bound.TaskID,
		CommandID: body.CommandID, AgentSessionID: bound.ID, VMID: bound.VMID,
	}, body); err != nil {
		writeError(response, http.StatusForbidden, "PERMISSION_DENIED", "workspace commit receipt is not command-scoped")
		return
	}
	response.WriteHeader(http.StatusNoContent)
}

func verifyCommitSignature(request *http.Request, receipt sourcev2.AgentCommitReceipt) bool {
	if request == nil || request.TLS == nil || len(request.TLS.PeerCertificates) == 0 {
		return false
	}
	publicKey, ok := request.TLS.PeerCertificates[0].PublicKey.(*ecdsa.PublicKey)
	if !ok {
		return false
	}
	canonical, err := receipt.Statement.Canonical()
	if err != nil {
		return false
	}
	digest := sha256.Sum256(canonical)
	if receipt.Attestation.StatementDigest != "sha256:"+hex.EncodeToString(digest[:]) {
		return false
	}
	signature, err := base64.StdEncoding.Strict().DecodeString(receipt.Signature)
	return err == nil && ecdsa.VerifyASN1(publicKey, digest[:], signature)
}

func (h Handler) planReceipt(response http.ResponseWriter, request *http.Request) {
	principal, ok := h.authenticate(response, request)
	if !ok {
		return
	}
	if h.PlanReceipts == nil {
		writeError(response, http.StatusServiceUnavailable, "UNAVAILABLE", "workspace plan receipt service unavailable")
		return
	}
	var body infrastructurev1.AgentPlanReceipt
	if decode(request, 8<<20+(64<<10), &body) != nil || body.Validate() != nil {
		writeError(response, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid workspace plan receipt")
		return
	}
	bound, err := h.Registry.AuthorizeOutcomeFor(request.Context(), principal, body.SessionID, body.ExecutionSessionID)
	if err != nil {
		writeSessionError(response, err)
		return
	}
	if err := h.PlanReceipts.RecordPlanReceipt(request.Context(), workspace.CredentialResolveRequest{
		TenantID: bound.TenantID, ProjectID: bound.ProjectID, WorkspaceID: bound.WorkspaceID, TaskID: bound.TaskID,
		CommandID: body.CommandID, AgentSessionID: bound.ID, VMID: bound.VMID,
	}, body); err != nil {
		writeError(response, http.StatusForbidden, "PERMISSION_DENIED", "workspace plan receipt is not command-scoped")
		return
	}
	response.WriteHeader(http.StatusNoContent)
}

func (h Handler) outputChunk(response http.ResponseWriter, request *http.Request) {
	principal, ok := h.authenticate(response, request)
	if !ok {
		return
	}
	if h.Outputs == nil {
		writeError(response, http.StatusServiceUnavailable, "UNAVAILABLE", "workspace output service unavailable")
		return
	}
	var body workspacev1.AgentOutputChunk
	if decode(request, h.bodyLimit(), &body) != nil || body.Validate() != nil {
		writeError(response, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid workspace output chunk")
		return
	}
	bound, err := h.Registry.AuthorizeOutcomeFor(request.Context(), principal, body.SessionID, body.ExecutionSessionID)
	if err != nil {
		writeSessionError(response, err)
		return
	}
	if err := h.Outputs.RecordOutputChunk(request.Context(), workspace.CredentialResolveRequest{
		TenantID: bound.TenantID, ProjectID: bound.ProjectID, WorkspaceID: bound.WorkspaceID, TaskID: bound.TaskID,
		CommandID: body.CommandID, AgentSessionID: bound.ID, VMID: bound.VMID,
	}, body); err != nil {
		writeError(response, http.StatusForbidden, "PERMISSION_DENIED", "workspace output chunk is not command-scoped")
		return
	}
	response.WriteHeader(http.StatusNoContent)
}

func (h Handler) resolveCredentials(response http.ResponseWriter, request *http.Request) {
	principal, ok := h.authenticate(response, request)
	if !ok {
		return
	}
	if h.Credentials == nil {
		writeError(response, http.StatusServiceUnavailable, "UNAVAILABLE", "workspace credential service unavailable")
		return
	}
	var body workspacev1.AgentCredentialResolve
	if decode(request, h.bodyLimit(), &body) != nil || strings.TrimSpace(body.CommandID) == "" {
		writeError(response, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid workspace credential request")
		return
	}
	bound, err := h.Registry.AuthorizeOutcomeFor(request.Context(), principal, body.SessionID, body.ExecutionSessionID)
	if err != nil {
		writeSessionError(response, err)
		return
	}
	view, err := h.Credentials.ResolveCredentials(request.Context(), workspace.CredentialResolveRequest{
		TenantID: bound.TenantID, ProjectID: bound.ProjectID, WorkspaceID: bound.WorkspaceID, TaskID: bound.TaskID,
		CommandID: body.CommandID, AgentSessionID: bound.ID, VMID: bound.VMID,
	})
	if err != nil {
		writeError(response, http.StatusForbidden, "PERMISSION_DENIED", "workspace credential request is not command-scoped")
		return
	}
	writeJSON(response, http.StatusOK, view)
}

func (h Handler) rotateCertificate(response http.ResponseWriter, request *http.Request) {
	principal, ok := h.authenticate(response, request)
	if !ok {
		return
	}
	var body workspacev1.AgentCertificateRotate
	if decode(request, h.bodyLimit(), &body) != nil || h.Certificates == nil || strings.TrimSpace(body.SessionID) == "" || len(body.CSRPEM) > 32<<10 {
		writeError(response, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid workspace certificate rotation")
		return
	}
	bound, err := h.Registry.AuthorizeOutcome(request.Context(), principal, body.SessionID)
	if err != nil {
		writeSessionError(response, err)
		return
	}
	bundle, err := h.Certificates.Sign(request.Context(), bootstrap.Identity{
		TenantID: bound.TenantID, ProjectID: bound.ProjectID, WorkspaceID: bound.WorkspaceID,
		TaskID: bound.TaskID, AgentID: bound.AgentID,
	}, []byte(body.CSRPEM))
	if err != nil {
		writeError(response, http.StatusServiceUnavailable, "UNAVAILABLE", "workspace certificate rotation failed")
		return
	}
	defer bundle.Clear()
	writeJSON(response, http.StatusOK, workspacev1.AgentCertificateView{
		CertificatePEM: string(bundle.Certificate), CAChainPEM: string(bundle.CAChain), NotAfter: bundle.NotAfter,
	})
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
	bound, err := h.Registry.AuthorizeOutcomeFor(request.Context(), principal, body.SessionID, body.ExecutionSessionID)
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
