package session

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/keir-research/ai-native-paas/internal/workspace"
	agentv2 "github.com/keir-research/ai-native-paas/pkg/contracts/agent/v2"
	workspacev1 "github.com/keir-research/ai-native-paas/pkg/contracts/workspace/v1"
)

const (
	defaultMaximumCertificateLifetime = 15 * time.Minute
	defaultSessionIdleTTL             = 75 * time.Second
	defaultDeliveryLease              = 30 * time.Second
	defaultDispatchAckTimeout         = 30 * time.Second
	defaultAckPollInterval            = 100 * time.Millisecond
)

type Registry struct {
	Store                      Store
	Clock                      workspace.Clock
	IDs                        workspace.IDGenerator
	MaximumCertificateLifetime time.Duration
	SessionIdleTTL             time.Duration
	DeliveryLease              time.Duration
	DispatchAckTimeout         time.Duration
	AckPollInterval            time.Duration
}

func (r *Registry) Connect(ctx context.Context, principal Principal, vmID string) (workspacev1.AgentSessionView, error) {
	if err := r.require(); err != nil {
		return workspacev1.AgentSessionView{}, err
	}
	now := r.Clock.Now().UTC()
	if err := principal.Validate(now, r.maximumCertificateLifetime()); err != nil || !validVMID(vmID) {
		return workspacev1.AgentSessionView{}, errors.New("workspace agent connection is invalid")
	}
	candidate := Session{
		ID: r.IDs.New("workspace-session"), TenantID: principal.TenantID, ProjectID: principal.ProjectID,
		WorkspaceID: principal.WorkspaceID, TaskID: principal.TaskID, AgentID: principal.AgentID,
		VMID: vmID, CertificateID: principal.CertificateID, ConnectedAt: now, LastSeenAt: now,
		ExpiresAt: principal.NotAfter.UTC(), Version: 1,
	}
	stored, err := r.Store.Connect(ctx, candidate)
	if err != nil {
		return workspacev1.AgentSessionView{}, err
	}
	return workspacev1.AgentSessionView{SessionID: stored.ID, WorkspaceID: stored.WorkspaceID, ExpiresAt: stored.ExpiresAt}, nil
}

func (r *Registry) Heartbeat(ctx context.Context, principal Principal, sessionID string) error {
	if err := r.require(); err != nil {
		return err
	}
	if _, err := r.authorize(ctx, principal, sessionID, true); err != nil {
		return err
	}
	_, err := r.Store.Touch(ctx, sessionID, principal.CertificateID, r.Clock.Now().UTC())
	return err
}

func (r *Registry) Connected(ctx context.Context, workspaceID, vmID string) (bool, error) {
	if err := r.require(); err != nil {
		return false, err
	}
	if strings.TrimSpace(workspaceID) == "" || !validVMID(vmID) {
		return false, nil
	}
	_, err := r.Store.ActiveForWorkspace(ctx, workspaceID, vmID, r.Clock.Now().UTC(), r.sessionIdleTTL())
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	return err == nil, err
}

func (r *Registry) Dispatch(ctx context.Context, envelope workspace.CommandEnvelope) (workspace.DispatchReceipt, error) {
	if err := r.require(); err != nil {
		return workspace.DispatchReceipt{}, err
	}
	if strings.TrimSpace(envelope.CommandID) == "" || strings.TrimSpace(envelope.WorkspaceID) == "" || strings.TrimSpace(envelope.ProjectID) == "" || strings.TrimSpace(envelope.TaskID) == "" || envelope.Spec.Validate() != nil {
		return workspace.DispatchReceipt{}, errors.New("workspace command envelope is invalid")
	}
	session, err := r.Store.ActiveForWorkspace(ctx, envelope.WorkspaceID, "", r.Clock.Now().UTC(), r.sessionIdleTTL())
	if err != nil {
		return workspace.DispatchReceipt{}, err
	}
	if session.ProjectID != envelope.ProjectID || session.TaskID != envelope.TaskID {
		return workspace.DispatchReceipt{}, ErrConflict
	}
	spec := envelope.Spec
	payload := workspacev1.AgentMessage{Kind: workspacev1.AgentMessageExec, CommandID: envelope.CommandID, WorkspaceID: envelope.WorkspaceID, Spec: &spec, CredentialLeases: append([]string(nil), envelope.CredentialLeases...)}
	message, err := r.queue(ctx, payload)
	if err != nil {
		return workspace.DispatchReceipt{}, err
	}
	deadline := r.dispatchAckTimeout()
	waitCtx, cancel := context.WithTimeout(ctx, deadline)
	defer cancel()
	ticker := time.NewTicker(r.ackPollInterval())
	defer ticker.Stop()
	for {
		stored, getErr := r.Store.GetMessage(waitCtx, message.ID)
		if getErr != nil {
			return workspace.DispatchReceipt{}, getErr
		}
		switch stored.State {
		case MessageAcked:
			return workspace.DispatchReceipt{CommandID: stored.CommandID, WorkspaceID: stored.WorkspaceID, VMID: stored.VMID, AgentSessionID: stored.SessionID, Accepted: true}, nil
		case MessageRejected:
			return workspace.DispatchReceipt{}, errors.New("workspace agent rejected command without execution")
		}
		select {
		case <-waitCtx.Done():
			return workspace.DispatchReceipt{}, fmt.Errorf("wait for workspace agent acknowledgement: %w", waitCtx.Err())
		case <-ticker.C:
		}
	}
}

func (r *Registry) RequestCancel(ctx context.Context, workspaceID, commandID string) error {
	if err := r.require(); err != nil {
		return err
	}
	if strings.TrimSpace(workspaceID) == "" || strings.TrimSpace(commandID) == "" {
		return errors.New("workspace cancellation identity is invalid")
	}
	if _, err := r.Store.ActiveForWorkspace(ctx, workspaceID, "", r.Clock.Now().UTC(), r.sessionIdleTTL()); err != nil {
		return err
	}
	_, err := r.queue(ctx, workspacev1.AgentMessage{Kind: workspacev1.AgentMessageCancel, CommandID: commandID, WorkspaceID: workspaceID})
	return err
}

func (r *Registry) Close(ctx context.Context, workspaceID string) error {
	if err := r.require(); err != nil {
		return err
	}
	if strings.TrimSpace(workspaceID) == "" {
		return errors.New("workspace session identity is invalid")
	}
	return r.Store.CloseWorkspace(ctx, workspaceID, r.Clock.Now().UTC(), "workspace_destroy")
}

func (r *Registry) Next(ctx context.Context, principal Principal, sessionID string) (workspacev1.AgentMessage, error) {
	return r.WaitNext(ctx, principal, sessionID, 0)
}

func (r *Registry) WaitNext(ctx context.Context, principal Principal, sessionID string, maximumWait time.Duration) (workspacev1.AgentMessage, error) {
	if err := r.require(); err != nil {
		return workspacev1.AgentMessage{}, err
	}
	session, err := r.authorize(ctx, principal, sessionID, true)
	if err != nil {
		return workspacev1.AgentMessage{}, err
	}
	now := r.Clock.Now().UTC()
	if _, err := r.Store.Touch(ctx, session.ID, principal.CertificateID, now); err != nil {
		return workspacev1.AgentMessage{}, err
	}
	deadline := time.Now().Add(maximumWait)
	for {
		now = r.Clock.Now().UTC()
		message, claimErr := r.Store.ClaimNext(ctx, session.ID, session.WorkspaceID, now, now.Add(r.deliveryLease()))
		if claimErr == nil {
			return message.View(), nil
		}
		if !errors.Is(claimErr, ErrNotFound) || maximumWait <= 0 || time.Until(deadline) <= 0 {
			return workspacev1.AgentMessage{}, claimErr
		}
		wait := r.ackPollInterval()
		if remaining := time.Until(deadline); wait > remaining {
			wait = remaining
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return workspacev1.AgentMessage{}, ctx.Err()
		case <-timer.C:
		}
	}
}

func (r *Registry) Acknowledge(ctx context.Context, principal Principal, sessionID, messageID string, accepted bool) error {
	if err := r.require(); err != nil {
		return err
	}
	session, err := r.authorize(ctx, principal, sessionID, true)
	if err != nil {
		return err
	}
	_, err = r.Store.Acknowledge(ctx, session.ID, session.WorkspaceID, messageID, accepted, r.Clock.Now().UTC())
	return err
}

func (r *Registry) AuthorizeOutcome(ctx context.Context, principal Principal, sessionID string) (Session, error) {
	if err := r.require(); err != nil {
		return Session{}, err
	}
	return r.authorize(ctx, principal, sessionID, true)
}

// AuthorizeOutcomeFor permits a freshly rotated certificate/session to report
// the result of a command that was accepted by a prior rotated session. It does
// not permit migration to another VM, principal, workspace, or arbitrary
// closed session.
func (r *Registry) AuthorizeOutcomeFor(ctx context.Context, principal Principal, authenticatedSessionID, executionSessionID string) (Session, error) {
	current, err := r.AuthorizeOutcome(ctx, principal, authenticatedSessionID)
	if err != nil {
		return Session{}, err
	}
	if strings.TrimSpace(executionSessionID) == "" || executionSessionID == current.ID {
		return current, nil
	}
	execution, err := r.Store.GetSession(ctx, executionSessionID)
	if err != nil {
		return Session{}, err
	}
	if execution.TenantID != current.TenantID || execution.ProjectID != current.ProjectID || execution.WorkspaceID != current.WorkspaceID || execution.TaskID != current.TaskID || execution.AgentID != current.AgentID || execution.VMID != current.VMID || execution.ClosedAt == nil || execution.CloseReason != "certificate_rotated" {
		return Session{}, ErrConflict
	}
	return execution, nil
}

func (r *Registry) queue(ctx context.Context, payload workspacev1.AgentMessage) (Message, error) {
	hash, err := agentv2.StableFingerprint(payload)
	if err != nil {
		return Message{}, err
	}
	now := r.Clock.Now().UTC()
	candidate := Message{
		ID: r.IDs.New("workspace-message"), WorkspaceID: payload.WorkspaceID, CommandID: payload.CommandID,
		Kind: payload.Kind, Payload: payload, PayloadHash: hash, State: MessageQueued, CreatedAt: now,
	}
	stored, _, err := r.Store.Queue(ctx, candidate)
	return stored, err
}

func (r *Registry) authorize(ctx context.Context, principal Principal, sessionID string, requireActive bool) (Session, error) {
	now := r.Clock.Now().UTC()
	if err := principal.Validate(now, r.maximumCertificateLifetime()); err != nil || strings.TrimSpace(sessionID) == "" {
		return Session{}, errors.New("workspace session credential is invalid")
	}
	session, err := r.Store.GetSession(ctx, sessionID)
	if err != nil {
		return Session{}, err
	}
	if session.TenantID != principal.TenantID || session.ProjectID != principal.ProjectID || session.WorkspaceID != principal.WorkspaceID || session.TaskID != principal.TaskID || session.AgentID != principal.AgentID || session.CertificateID != principal.CertificateID {
		return Session{}, ErrConflict
	}
	if requireActive && !session.Active(now, r.sessionIdleTTL()) {
		return Session{}, ErrNotFound
	}
	return session, nil
}

func (r *Registry) require() error {
	if r == nil || r.Store == nil || r.Clock == nil || r.IDs == nil {
		return errors.New("workspace session registry dependencies are unavailable")
	}
	return nil
}

func (r *Registry) maximumCertificateLifetime() time.Duration {
	if r.MaximumCertificateLifetime <= 0 {
		return defaultMaximumCertificateLifetime
	}
	return r.MaximumCertificateLifetime
}
func (r *Registry) sessionIdleTTL() time.Duration {
	if r.SessionIdleTTL <= 0 {
		return defaultSessionIdleTTL
	}
	return r.SessionIdleTTL
}
func (r *Registry) deliveryLease() time.Duration {
	if r.DeliveryLease <= 0 {
		return defaultDeliveryLease
	}
	return r.DeliveryLease
}
func (r *Registry) dispatchAckTimeout() time.Duration {
	if r.DispatchAckTimeout <= 0 {
		return defaultDispatchAckTimeout
	}
	return r.DispatchAckTimeout
}
func (r *Registry) ackPollInterval() time.Duration {
	if r.AckPollInterval <= 0 {
		return defaultAckPollInterval
	}
	return r.AckPollInterval
}
