package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/keir-research/ai-native-paas/internal/agent/domain"
	agentv1 "github.com/keir-research/ai-native-paas/pkg/contracts/agent/v1"
	agentv2 "github.com/keir-research/ai-native-paas/pkg/contracts/agent/v2"
	commercev1 "github.com/keir-research/ai-native-paas/pkg/contracts/commerce/v1"
	"strings"
	"time"
)

type RegisterPrincipalCommand struct {
	ID, TenantID, OnBehalfOfUserID string
	Scopes                         []string
	CredentialExpiresAt            time.Time
}
type StartTaskCommand struct {
	ID, TenantID, ProjectID, AgentID, OnBehalfOfUserID, CorrelationID string
	IntentID                                                          string
	BudgetPolicy                                                      agentv1.BudgetPolicy
}

// RecordTaskEvidenceCommand is an internal, trusted-producer contract. Its
// identity fields must come from verified Project MCP scope, never from tool
// arguments supplied by an agent.
type RecordTaskEvidenceCommand struct {
	TenantID, ProjectID, AgentID, TaskID, CorrelationID string
	Tool                                                agentv2.Tool
	IdempotencyKey                                      string
	Evidence                                            agentv1.AuditEvidence
}

func (s *Service) RegisterPrincipal(ctx context.Context, c RegisterPrincipalCommand) (domain.AgentPrincipal, error) {
	if err := s.require(); err != nil {
		return domain.AgentPrincipal{}, err
	}
	now := s.now()
	p := domain.AgentPrincipal{ID: c.ID, TenantID: c.TenantID, OnBehalfOfUserID: c.OnBehalfOfUserID, Scopes: append([]string(nil), c.Scopes...), State: domain.PrincipalActive, CredentialExpiresAt: c.CredentialExpiresAt, Version: 1, CreatedAt: now, UpdatedAt: now}
	if p.CredentialExpiresAt.IsZero() {
		p.CredentialExpiresAt = now.Add(time.Hour)
	}
	if err := p.Validate(); err != nil {
		return p, domain.Wrap(domain.CodeInvalidArgument, "invalid agent principal", err)
	}
	err := s.Store.Transact(ctx, func(tx Tx) error {
		if _, ok := tx.GetPrincipal(p.ID); ok {
			return domain.NewError(domain.CodeConflict, "agent principal exists")
		}
		return tx.InsertPrincipal(p)
	})
	return p, err
}
func (s *Service) StartTask(ctx context.Context, c StartTaskCommand) (domain.AgentTask, error) {
	if err := s.require(); err != nil {
		return domain.AgentTask{}, err
	}
	if c.BudgetPolicy.Validate() != nil || !agentv1.ValidID(c.ID) || !agentv1.ValidID(c.TenantID) || c.ProjectID != "" && !agentv1.ValidID(c.ProjectID) || !agentv1.ValidID(c.AgentID) || !agentv1.ValidID(c.OnBehalfOfUserID) || !agentv1.ValidID(c.CorrelationID) || c.IntentID != "" && !agentv1.ValidID(c.IntentID) {
		return domain.AgentTask{}, domain.NewError(domain.CodeInvalidArgument, "invalid task identity or budget")
	}
	now := s.now()
	t := domain.AgentTask{ID: c.ID, TenantID: c.TenantID, ProjectID: c.ProjectID, AgentID: c.AgentID, OnBehalfOfUserID: c.OnBehalfOfUserID, CorrelationID: c.CorrelationID, State: domain.TaskActive, BudgetPolicy: c.BudgetPolicy, Version: 1, CreatedAt: now, UpdatedAt: now}
	err := s.Store.Transact(ctx, func(tx Tx) error {
		p, ok := tx.GetPrincipal(c.AgentID)
		if !ok {
			return domain.NewError(domain.CodeNotFound, "agent principal not found")
		}
		if p.TenantID != c.TenantID || p.OnBehalfOfUserID != c.OnBehalfOfUserID {
			return domain.NewError(domain.CodePermissionDenied, "task identity denied")
		}
		if _, ok := tx.GetTask(c.ID); ok {
			return domain.NewError(domain.CodeConflict, "task exists")
		}
		if err := tx.InsertTask(t); err != nil {
			return err
		}
		return tx.AppendAudit(domain.AuditRecord{
			ID: s.newID("audit"), TenantID: t.TenantID, TaskID: t.ID, AgentID: t.AgentID,
			OnBehalfOfUserID: t.OnBehalfOfUserID, Action: "task_start", CorrelationID: t.CorrelationID,
			Outcome: "STARTED", ResourceType: "task", ResourceID: t.ID,
			Evidence: agentv1.AuditEvidence{IntentID: c.IntentID, ProjectID: c.ProjectID}, CreatedAt: now,
		})
	})
	return t, err
}

// RecordTaskEvidence appends one idempotent, allowlisted event to the task
// timeline. It lets Project MCP contribute workspace and infrastructure facts
// to the same chain used by the legacy Agent façade without accepting raw
// command arguments or provider payloads.
func (s *Service) RecordTaskEvidence(ctx context.Context, c RecordTaskEvidenceCommand) error {
	if err := s.require(); err != nil {
		return err
	}
	if !agentv1.ValidID(c.TenantID) || !agentv1.ValidID(c.ProjectID) || !agentv1.ValidID(c.AgentID) || !agentv1.ValidID(c.TaskID) || !agentv1.ValidID(c.CorrelationID) || strings.TrimSpace(c.IdempotencyKey) == "" || len(c.IdempotencyKey) > 128 || !agentv2.ValidTool(c.Tool) {
		return domain.NewError(domain.CodeInvalidArgument, "invalid task evidence identity")
	}
	if c.Evidence.ProjectID != "" && c.Evidence.ProjectID != c.ProjectID {
		return domain.NewError(domain.CodePermissionDenied, "task evidence project mismatch")
	}
	c.Evidence.ProjectID = c.ProjectID
	if err := c.Evidence.Validate(); err != nil {
		return domain.Wrap(domain.CodeInvalidArgument, "invalid task evidence", err)
	}
	fingerprint, err := json.Marshal(struct {
		TenantID, ProjectID, AgentID, TaskID, CorrelationID, Tool, IdempotencyKey string
	}{c.TenantID, c.ProjectID, c.AgentID, c.TaskID, c.CorrelationID, string(c.Tool), c.IdempotencyKey})
	if err != nil {
		return domain.Wrap(domain.CodeInvalidArgument, "invalid task evidence", err)
	}
	sum := sha256.Sum256(fingerprint)
	auditID := "audit-evidence-" + hex.EncodeToString(sum[:16])
	return s.Store.Transact(ctx, func(tx Tx) error {
		task, ok := tx.GetTask(c.TaskID)
		if !ok || task.TenantID != c.TenantID || task.ProjectID == "" || task.ProjectID != c.ProjectID || task.AgentID != c.AgentID || task.CorrelationID != c.CorrelationID {
			return domain.NewError(domain.CodePermissionDenied, "task evidence scope denied")
		}
		record := domain.AuditRecord{
			ID: auditID, TenantID: c.TenantID, TaskID: c.TaskID, AgentID: c.AgentID,
			OnBehalfOfUserID: task.OnBehalfOfUserID, Action: string(c.Tool), CorrelationID: c.CorrelationID,
			Outcome: "SUCCEEDED", Evidence: c.Evidence, CreatedAt: s.now(),
		}
		record.ResourceType, record.ResourceID = evidenceResource(c.Evidence)
		for _, existing := range tx.ListAudit(c.TenantID, c.TaskID) {
			if existing.ID != auditID {
				continue
			}
			if existing.TenantID == record.TenantID && existing.TaskID == record.TaskID && existing.AgentID == record.AgentID && existing.OnBehalfOfUserID == record.OnBehalfOfUserID && existing.Action == record.Action && existing.CorrelationID == record.CorrelationID && existing.Outcome == record.Outcome && existing.ResourceType == record.ResourceType && existing.ResourceID == record.ResourceID && existing.Evidence == record.Evidence {
				return nil
			}
			return domain.NewError(domain.CodeConflict, "task evidence idempotency conflict")
		}
		return tx.AppendAudit(record)
	})
}

func evidenceResource(e agentv1.AuditEvidence) (string, string) {
	for _, candidate := range []struct{ kind, id string }{
		{"deployment", e.DeploymentID}, {"release", e.ReleaseID}, {"apply_operation", e.ApplyOperationID},
		{"approval_grant", e.ApprovalGrantID}, {"approval_request", e.ApprovalRequestID}, {"plan", e.PlanID},
		{"build", e.BuildID}, {"commit", e.CommitSHA}, {"workspace_command", e.WorkspaceCommandID},
		{"workspace", e.WorkspaceID}, {"project", e.ProjectID},
	} {
		if candidate.id != "" {
			return candidate.kind, candidate.id
		}
	}
	return "task", ""
}
func (s *Service) GetTask(ctx context.Context, tenant, id string) (agentv1.TaskView, error) {
	if err := s.require(); err != nil {
		return agentv1.TaskView{}, err
	}
	var out agentv1.TaskView
	err := s.Store.Transact(ctx, func(tx Tx) error {
		t, ok := tx.GetTask(id)
		if !ok || t.TenantID != tenant {
			return domain.NewError(domain.CodeNotFound, "task not found")
		}
		out = t.View()
		return nil
	})
	return out, err
}
func (s *Service) ResumeTask(ctx context.Context, tenant, id, actor string) error {
	if err := s.require(); err != nil {
		return err
	}
	return s.Store.Transact(ctx, func(tx Tx) error {
		t, ok := tx.GetTask(id)
		if !ok || t.TenantID != tenant {
			return domain.NewError(domain.CodeNotFound, "task not found")
		}
		if t.State != domain.TaskPaused {
			return domain.NewError(domain.CodeConflict, "task is not paused")
		}
		t.State = domain.TaskActive
		t.RepairCount = 0
		t.RepairFingerprint = ""
		old := t.Version
		t.Version++
		t.UpdatedAt = s.now()
		if err := tx.UpdateTask(t, old); err != nil {
			return err
		}
		return tx.AppendAudit(domain.AuditRecord{ID: s.newID("audit"), TenantID: tenant, TaskID: id, AgentID: t.AgentID, OnBehalfOfUserID: t.OnBehalfOfUserID, CorrelationID: t.CorrelationID, Outcome: "SUCCEEDED", ResourceType: "task", ResourceID: id, CreatedAt: s.now()})
	})
}
func (s *Service) AuditTrail(ctx context.Context, tenant, task string) ([]agentv1.AuditView, error) {
	if err := s.require(); err != nil {
		return nil, err
	}
	var out []agentv1.AuditView
	err := s.Store.Transact(ctx, func(tx Tx) error {
		t, ok := tx.GetTask(task)
		if !ok || t.TenantID != tenant {
			return domain.NewError(domain.CodeNotFound, "task not found")
		}
		for _, a := range tx.ListAudit(tenant, task) {
			out = append(out, a.View())
		}
		return nil
	})
	return out, err
}

func (s *Service) Invoke(ctx context.Context, req agentv1.InvocationRequest) (agentv1.InvocationResponse, error) {
	if err := s.require(); err != nil {
		return s.failureResponse("inv-invalid", err), err
	}
	if err := req.Validate(); err != nil {
		e := domain.Wrap(domain.CodeInvalidArgument, "invalid invocation request", err)
		return s.failureResponse("inv-invalid", e), e
	}
	if err := validateToolArguments(req.Tool, req.Arguments); err != nil {
		e := domain.Wrap(domain.CodeInvalidArgument, "invalid tool arguments", err)
		_ = s.auditDenied(ctx, req, e)
		return s.failureResponse("inv-invalid", e), e
	}
	fp, err := agentv1.StableFingerprint(struct {
		Tool      agentv1.Tool    `json:"tool"`
		Arguments json.RawMessage `json:"arguments"`
	}{req.Tool, req.Arguments})
	if err != nil {
		return s.failureResponse("inv-invalid", err), err
	}
	var inv domain.Invocation
	var principal domain.AgentPrincipal
	var task domain.AgentTask
	var replay *agentv1.InvocationResponse
	var existingStarted bool
	var budgetErr error
	err = s.Store.Transact(ctx, func(tx Tx) error {
		budgetErr = nil
		p, ok := tx.GetPrincipal(req.AgentID)
		if !ok || p.TenantID != req.TenantID || p.State != domain.PrincipalActive || (!p.CredentialExpiresAt.IsZero() && !p.CredentialExpiresAt.After(s.now())) {
			return domain.NewError(domain.CodePermissionDenied, "agent principal denied")
		}
		t, ok := tx.GetTask(req.TaskID)
		if !ok || t.TenantID != req.TenantID || t.AgentID != req.AgentID || t.OnBehalfOfUserID != p.OnBehalfOfUserID {
			return domain.NewError(domain.CodePermissionDenied, "agent task denied")
		}
		if t.State == domain.TaskPaused && !agentv1.IsReadOnly(req.Tool) {
			return domain.NewError(domain.CodePaused, "agent task is paused")
		}
		if t.State == domain.TaskCanceled {
			return domain.NewError(domain.CodeCanceled, "agent task canceled")
		}
		if !p.HasScope(string(agentv1.ScopeForTool(req.Tool))) {
			return domain.NewError(domain.CodePermissionDenied, "tool scope denied")
		}
		if old, ok := tx.FindInvocation(req.TenantID, req.TaskID, req.IdempotencyKey); ok {
			if old.Fingerprint != fp || old.Tool != req.Tool {
				return domain.NewError(domain.CodeConflict, "idempotency key payload conflict")
			}
			inv = old
			principal = p
			task = t
			if len(old.Response) > 0 {
				var v agentv1.InvocationResponse
				if json.Unmarshal(old.Response, &v) == nil {
					v.Replayed = true
					replay = &v
				}
			} else {
				existingStarted = true
			}
			return nil
		}
		if err := s.checkEntitlement(ctx, req); err != nil {
			return err
		}
		if err := s.consumeApproval(tx, req, p, t); err != nil {
			return err
		}
		if err := reserveBudget(&t, req); err != nil {
			budgetErr = err
			old := t.Version
			t.State = domain.TaskPaused
			t.Version++
			t.UpdatedAt = s.now()
			if err := tx.UpdateTask(t, old); err != nil {
				return err
			}
			principal = p
			task = t
			return nil
		}
		if t.Version > 0 {
			old := t.Version
			t.Version++
			t.UpdatedAt = s.now()
			if err := tx.UpdateTask(t, old); err != nil {
				return err
			}
		}
		inv = domain.Invocation{ID: s.newID("inv"), TenantID: req.TenantID, AgentID: req.AgentID, TaskID: req.TaskID, Tool: req.Tool, IdempotencyKey: req.IdempotencyKey, Fingerprint: fp, State: domain.InvocationStarted, Version: 1, CreatedAt: s.now(), UpdatedAt: s.now()}
		if err := tx.InsertInvocation(inv); err != nil {
			return err
		}
		principal = p
		task = t
		return tx.AppendOutbox(domain.OutboxRecord{ID: s.newID("evt"), TenantID: req.TenantID, Topic: "agent.tool_invoked.v1", AggregateID: inv.ID, Payload: raw(map[string]any{"tool": req.Tool, "task_id": req.TaskID}), CreatedAt: s.now()})
	})
	if err != nil {
		_ = s.auditDenied(ctx, req, err)
		return s.failureResponse(s.newID("inv"), err), err
	}
	if budgetErr != nil {
		_ = s.auditDenied(ctx, req, budgetErr)
		return s.failureResponse(s.newID("inv"), budgetErr), budgetErr
	}
	if replay != nil {
		return *replay, nil
	}
	_ = principal
	_ = task
	_ = existingStarted
	resp, execErr := s.execute(ctx, req, inv.ID)
	if resp.InvocationID == "" {
		resp.InvocationID = inv.ID
	}
	resp.APIVersion = agentv1.APIVersion
	finalErr := s.finalize(ctx, req, inv, resp, execErr)
	if execErr != nil {
		return resp, execErr
	}
	if finalErr != nil {
		return s.failureResponse(inv.ID, finalErr), finalErr
	}
	return resp, nil
}

func reserveBudget(t *domain.AgentTask, req agentv1.InvocationRequest) error {
	switch req.Tool {
	case agentv1.ToolRequestBuild:
		var a RequestBuildArguments
		if agentv1.DecodeStrict(req.Arguments, &a) != nil {
			return domain.NewError(domain.CodeInvalidArgument, "invalid build")
		}
		if t.BudgetUsage.BuildCount+1 > t.BudgetPolicy.MaxBuildCount {
			return domain.NewError(domain.CodeBudgetExceeded, "build count budget exceeded")
		}
		if t.BudgetUsage.BuildMinutes+a.EstimatedMinutes > t.BudgetPolicy.MaxBuildMinutes {
			return domain.NewError(domain.CodeBudgetExceeded, "build minutes budget exceeded")
		}
		t.BudgetUsage.BuildCount++
		t.BudgetUsage.BuildMinutes += a.EstimatedMinutes
	case agentv1.ToolDeploy, agentv1.ToolRollback:
		if t.BudgetUsage.DeployCount+1 > t.BudgetPolicy.MaxDeployCount {
			return domain.NewError(domain.CodeBudgetExceeded, "deploy count budget exceeded")
		}
		t.BudgetUsage.DeployCount++
	}
	return nil
}
func (s *Service) checkEntitlement(ctx context.Context, req agentv1.InvocationRequest) error {
	if agentv1.IsReadOnly(req.Tool) || req.Tool == agentv1.ToolRequestApproval {
		return nil
	}
	if s.Commerce == nil {
		return domain.NewError(domain.CodeUnavailable, "commercial entitlement service unavailable")
	}
	d, err := s.Commerce.Check(ctx, commercev1.EntitlementRequest{TenantID: req.TenantID, Feature: string(req.Tool), Resource: req.TaskID, Quantity: 1, At: s.now()})
	if err != nil {
		return domain.Wrap(domain.CodeUnavailable, "commercial entitlement unavailable", err)
	}
	if !d.Allowed {
		return domain.NewError(domain.CodeEntitlementDenied, "action is not permitted by the active plan")
	}
	return nil
}
func (s *Service) consumeApproval(tx Tx, req agentv1.InvocationRequest, p domain.AgentPrincipal, t domain.AgentTask) error {
	action, res, required := approvalRequirement(req)
	if !required {
		return nil
	}
	if req.ApprovalGrantID == "" {
		return domain.NewError(domain.CodeApprovalRequired, "matching user approval is required")
	}
	g, ok := tx.GetApprovalGrant(req.ApprovalGrantID)
	if !ok || g.TenantID != req.TenantID || g.AgentID != req.AgentID || g.TaskID != req.TaskID || g.Action != action || g.Resource != res || g.ConsumedAt != nil || !g.ExpiresAt.After(s.now()) {
		return domain.NewError(domain.CodePermissionDenied, "approval grant denied")
	}
	hash, _ := agentv1.StableFingerprint(req.Arguments)
	if g.PayloadHash != hash {
		return domain.NewError(domain.CodePermissionDenied, "approval payload mismatch")
	}
	now := s.now()
	g.ConsumedAt = &now
	old := g.Version
	g.Version++
	return tx.UpdateApprovalGrant(g, old)
}
func approvalRequirement(req agentv1.InvocationRequest) (agentv1.ApprovalAction, agentv1.ApprovalResource, bool) {
	switch req.Tool {
	case agentv1.ToolDeploy:
		var a DeployArguments
		if agentv1.DecodeStrict(req.Arguments, &a) == nil && strings.EqualFold(a.EnvironmentName, "production") {
			return agentv1.ApprovalDeployProduction, agentv1.ApprovalResource{Type: "environment", ID: a.EnvironmentID}, true
		}
	case agentv1.ToolRollback:
		var a RollbackArguments
		if agentv1.DecodeStrict(req.Arguments, &a) == nil {
			return agentv1.ApprovalDeployProduction, agentv1.ApprovalResource{Type: "deployment", ID: a.DeploymentID}, true
		}
	}
	return "", agentv1.ApprovalResource{}, false
}
func (s *Service) finalize(ctx context.Context, req agentv1.InvocationRequest, initial domain.Invocation, resp agentv1.InvocationResponse, execErr error) error {
	return s.Store.Transact(ctx, func(tx Tx) error {
		v, ok := tx.GetInvocation(initial.ID)
		if !ok {
			return domain.NewError(domain.CodeNotFound, "invocation not found")
		}
		encoded, _ := json.Marshal(resp)
		v.Response = encoded
		v.State = domain.InvocationCompleted
		if execErr != nil {
			v.State = domain.InvocationFailed
		}
		old := v.Version
		v.Version++
		v.UpdatedAt = s.now()
		if err := tx.UpdateInvocation(v, old); err != nil {
			return err
		}
		t, _ := tx.GetTask(req.TaskID)
		outcome := "SUCCEEDED"
		code := ""
		if execErr != nil {
			outcome = "FAILED"
			de := domain.AsError(execErr)
			code = string(de.Code)
			if !de.Retryable {
				fingerprint := string(de.Code) + ":" + string(req.Tool)
				if t.RepairFingerprint == fingerprint {
					t.RepairCount++
				} else {
					t.RepairFingerprint = fingerprint
					t.RepairCount = 1
				}
				if t.RepairCount >= t.BudgetPolicy.RepairThreshold {
					t.State = domain.TaskPaused
				}
				to := t.Version
				t.Version++
				t.UpdatedAt = s.now()
				if err := tx.UpdateTask(t, to); err != nil {
					return err
				}
			}
		}
		resourceType, resourceID, op := responseRefs(resp)
		return tx.AppendAudit(domain.AuditRecord{ID: s.newID("audit"), TenantID: req.TenantID, TaskID: req.TaskID, AgentID: req.AgentID, OnBehalfOfUserID: t.OnBehalfOfUserID, Tool: req.Tool, Action: string(req.Tool), CorrelationID: req.CorrelationID, Outcome: outcome, ResourceType: resourceType, ResourceID: resourceID, OperationID: op, ErrorCode: code, Evidence: invocationAuditEvidence(req, resp), CreatedAt: s.now()})
	})
}
func responseRefs(r agentv1.InvocationResponse) (string, string, string) {
	if r.Result != nil {
		return r.Result.Type, r.Result.ID, ""
	}
	if r.Operation != nil {
		return "operation", string(r.Operation.OperationID), string(r.Operation.OperationID)
	}
	return "", "", ""
}
func (s *Service) auditDenied(ctx context.Context, req agentv1.InvocationRequest, err error) error {
	if s.Store == nil {
		return nil
	}
	return s.Store.Transact(ctx, func(tx Tx) error {
		t, _ := tx.GetTask(req.TaskID)
		return tx.AppendAudit(domain.AuditRecord{ID: s.newID("audit"), TenantID: req.TenantID, TaskID: req.TaskID, AgentID: req.AgentID, OnBehalfOfUserID: t.OnBehalfOfUserID, Tool: req.Tool, Action: string(req.Tool), CorrelationID: req.CorrelationID, Outcome: "DENIED", ErrorCode: string(domain.AsError(err).Code), CreatedAt: s.now()})
	})
}
func (s *Service) failureResponse(id string, err error) agentv1.InvocationResponse {
	if !agentv1.ValidID(id) {
		id = "inv-failed"
	}
	de := domain.AsError(err)
	return agentv1.InvocationResponse{APIVersion: agentv1.APIVersion, InvocationID: id, Result: &agentv1.ResultReference{Type: "error", ID: id, State: "FAILED"}, Error: agentv1.PublicError(string(de.Code), safeMessage(de), de.Retryable, de.OperationID)}
}
func safeMessage(e *domain.Error) string {
	switch e.Code {
	case domain.CodeInvalidArgument, domain.CodePermissionDenied, domain.CodeNotFound, domain.CodeConflict, domain.CodeBudgetExceeded, domain.CodeApprovalRequired, domain.CodePaused, domain.CodeEntitlementDenied, domain.CodeCanceled:
		return e.Message
	default:
		return "platform operation is temporarily unavailable"
	}
}

func (s *Service) GrantApprovalForTenant(ctx context.Context, tenant, requestID, userID, kind string) (domain.ApprovalGrant, error) {
	if err := s.require(); err != nil {
		return domain.ApprovalGrant{}, err
	}
	if kind != "user" && kind != "operator" {
		return domain.ApprovalGrant{}, domain.NewError(domain.CodePermissionDenied, "human approver required")
	}
	var out domain.ApprovalGrant
	err := s.Store.Transact(ctx, func(tx Tx) error {
		r, ok := tx.GetApprovalRequest(requestID)
		if !ok || r.TenantID != tenant {
			return domain.NewError(domain.CodeNotFound, "approval request not found")
		}
		if r.AgentID == userID {
			return domain.NewError(domain.CodePermissionDenied, "agent cannot approve its own request")
		}
		if r.State != domain.ApprovalPending || !r.ExpiresAt.After(s.now()) {
			return domain.NewError(domain.CodeConflict, "approval request is not pending")
		}
		r.State = domain.ApprovalGranted
		old := r.Version
		r.Version++
		r.UpdatedAt = s.now()
		if err := tx.UpdateApprovalRequest(r, old); err != nil {
			return err
		}
		out = domain.ApprovalGrant{ID: s.newID("grant"), RequestID: r.ID, TenantID: r.TenantID, AgentID: r.AgentID, TaskID: r.TaskID, ApproverUserID: userID, Action: r.Action, Resource: r.Resource, PayloadHash: r.PayloadHash, ExpiresAt: r.ExpiresAt, Version: 1, CreatedAt: s.now()}
		if err := tx.InsertApprovalGrant(out); err != nil {
			return err
		}
		task, _ := tx.GetTask(r.TaskID)
		return tx.AppendAudit(domain.AuditRecord{
			ID: s.newID("audit"), TenantID: r.TenantID, TaskID: r.TaskID, AgentID: r.AgentID,
			OnBehalfOfUserID: task.OnBehalfOfUserID, Action: "approval_grant", CorrelationID: task.CorrelationID,
			Outcome: "GRANTED", ResourceType: "approval_grant", ResourceID: out.ID,
			Evidence: agentv1.AuditEvidence{ApprovalRequestID: r.ID, ApprovalGrantID: out.ID, ApprovalPayloadHash: r.PayloadHash}, CreatedAt: s.now(),
		})
	})
	return out, err
}
func (s *Service) DenyApprovalForTenant(ctx context.Context, tenant, requestID, userID, kind string) error {
	if kind != "user" && kind != "operator" {
		return domain.NewError(domain.CodePermissionDenied, "human approver required")
	}
	return s.Store.Transact(ctx, func(tx Tx) error {
		r, ok := tx.GetApprovalRequest(requestID)
		if !ok || r.TenantID != tenant {
			return domain.NewError(domain.CodeNotFound, "approval request not found")
		}
		if r.AgentID == userID {
			return domain.NewError(domain.CodePermissionDenied, "agent cannot deny its own request")
		}
		if r.State != domain.ApprovalPending {
			return domain.NewError(domain.CodeConflict, "approval request is not pending")
		}
		r.State = domain.ApprovalDenied
		old := r.Version
		r.Version++
		r.UpdatedAt = s.now()
		if err := tx.UpdateApprovalRequest(r, old); err != nil {
			return err
		}
		task, _ := tx.GetTask(r.TaskID)
		return tx.AppendAudit(domain.AuditRecord{
			ID: s.newID("audit"), TenantID: r.TenantID, TaskID: r.TaskID, AgentID: r.AgentID,
			OnBehalfOfUserID: task.OnBehalfOfUserID, Action: "approval_denial", CorrelationID: task.CorrelationID,
			Outcome: "DENIED", ResourceType: "approval_request", ResourceID: r.ID,
			Evidence: agentv1.AuditEvidence{ApprovalRequestID: r.ID, ApprovalPayloadHash: r.PayloadHash}, CreatedAt: s.now(),
		})
	})
}
func (s *Service) RecordFailure(ctx context.Context, tenant, taskID, fingerprint string, platform bool) error {
	return s.Store.Transact(ctx, func(tx Tx) error {
		t, ok := tx.GetTask(taskID)
		if !ok || t.TenantID != tenant {
			return domain.NewError(domain.CodeNotFound, "task not found")
		}
		if platform {
			return nil
		}
		if t.RepairFingerprint == fingerprint {
			t.RepairCount++
		} else {
			t.RepairFingerprint = fingerprint
			t.RepairCount = 1
		}
		if t.RepairCount >= t.BudgetPolicy.RepairThreshold {
			t.State = domain.TaskPaused
		}
		old := t.Version
		t.Version++
		t.UpdatedAt = s.now()
		return tx.UpdateTask(t, old)
	})
}

var _ = fmt.Sprintf
var _ = errors.Is
