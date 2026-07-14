package memory

import (
	"context"
	"sync"
	"time"

	infraapp "github.com/keir-research/ai-native-paas/internal/infrastructure/application"
	"github.com/keir-research/ai-native-paas/internal/workspace"
	infrastructurev1 "github.com/keir-research/ai-native-paas/pkg/contracts/infrastructure/v1"
)

type Store struct {
	mu          sync.Mutex
	plans       map[string]infraapp.PlanRecord
	idempotency map[string]string
	approvals   map[string]infraapp.ApprovalGrant
	receipts    map[string]infraapp.PlanReceiptRecord
}

func New() *Store {
	return &Store{plans: map[string]infraapp.PlanRecord{}, idempotency: map[string]string{}, approvals: map[string]infraapp.ApprovalGrant{}, receipts: map[string]infraapp.PlanReceiptRecord{}}
}

func (s *Store) PutPlanReceipt(_ context.Context, scope workspace.PlanReceiptScope, receipt infrastructurev1.AgentPlanReceipt) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if receipt.Validate() != nil || scope.TenantID == "" || scope.ProjectID == "" || scope.WorkspaceID == "" || scope.TaskID == "" || scope.CommandID != receipt.CommandID || scope.ActorID == "" {
		return infraapp.ErrPermissionDenied
	}
	candidate := infraapp.PlanReceiptRecord{
		TenantID: scope.TenantID, ProjectID: scope.ProjectID, WorkspaceID: scope.WorkspaceID,
		TaskID: scope.TaskID, CommandID: scope.CommandID, ActorID: scope.ActorID,
		ArtifactDigest: receipt.ArtifactDigest, PlanJSON: append([]byte(nil), receipt.PlanJSON...),
		CapturedAt: receipt.CapturedAt, ReceivedAt: receipt.CapturedAt,
	}
	if stored, ok := s.receipts[scope.CommandID]; ok {
		if stored.TenantID != candidate.TenantID || stored.ProjectID != candidate.ProjectID || stored.WorkspaceID != candidate.WorkspaceID || stored.ActorID != candidate.ActorID || stored.ArtifactDigest != candidate.ArtifactDigest || string(stored.PlanJSON) != string(candidate.PlanJSON) {
			return infraapp.ErrConflict
		}
		return nil
	}
	s.receipts[scope.CommandID] = candidate
	return nil
}

func (s *Store) GetPlanReceipt(_ context.Context, tenantID, projectID, commandID string) (infraapp.PlanReceiptRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	receipt, ok := s.receipts[commandID]
	if !ok || receipt.TenantID != tenantID || receipt.ProjectID != projectID {
		return infraapp.PlanReceiptRecord{}, infraapp.ErrNotFound
	}
	receipt.PlanJSON = append([]byte(nil), receipt.PlanJSON...)
	return receipt, nil
}

func (s *Store) CreatePlan(_ context.Context, record infraapp.PlanRecord) (infraapp.PlanRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := record.TenantID + "\x00" + record.Summary.ProjectID + "\x00" + record.IdempotencyKey
	if id, ok := s.idempotency[key]; ok {
		existing := s.plans[id]
		if existing.IdempotencyFingerprint != record.IdempotencyFingerprint {
			return infraapp.PlanRecord{}, infraapp.ErrConflict
		}
		return existing, nil
	}
	if _, ok := s.plans[record.Summary.PlanID]; ok {
		return infraapp.PlanRecord{}, infraapp.ErrConflict
	}
	s.plans[record.Summary.PlanID] = record
	s.idempotency[key] = record.Summary.PlanID
	return record, nil
}

func (s *Store) GetPlan(_ context.Context, tenantID, projectID, planID string) (infraapp.PlanRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.plans[planID]
	if !ok || record.TenantID != tenantID || record.Summary.ProjectID != projectID {
		return infraapp.PlanRecord{}, infraapp.ErrNotFound
	}
	return record, nil
}

func (s *Store) CreateApproval(_ context.Context, grant infraapp.ApprovalGrant) (infraapp.ApprovalGrant, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.approvals[grant.GrantID]; ok {
		return infraapp.ApprovalGrant{}, infraapp.ErrConflict
	}
	plan, ok := s.plans[grant.PlanID]
	if !ok || plan.TenantID != grant.TenantID || plan.Summary.ProjectID != grant.ProjectID || plan.RequestedByActorID != grant.ActorID || !plan.Summary.RequiresApproval || grant.CreatedAt.IsZero() || !grant.ExpiresAt.After(grant.CreatedAt) || grant.ExpiresAt.After(plan.Reservation.ExpiresAt) {
		return infraapp.ApprovalGrant{}, infraapp.ErrPermissionDenied
	}
	s.approvals[grant.GrantID] = grant
	return grant, nil
}

func (s *Store) GetActiveApproval(_ context.Context, tenantID, projectID, planID, actorID string, now time.Time) (infraapp.ApprovalGrant, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, grant := range s.approvals {
		if grant.TenantID == tenantID && grant.ProjectID == projectID && grant.PlanID == planID && grant.ActorID == actorID && grant.ConsumedAt.IsZero() && grant.ExpiresAt.After(now) {
			return grant, nil
		}
	}
	return infraapp.ApprovalGrant{}, infraapp.ErrNotFound
}

func (s *Store) AuthorizeApply(_ context.Context, match infraapp.ApplyMatch) (infraapp.PlanRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a := match.Authorization
	plan, ok := s.plans[a.PlanID]
	if !ok || plan.TenantID != match.TenantID || plan.Summary.ProjectID != match.ProjectID {
		return infraapp.PlanRecord{}, infraapp.ErrNotFound
	}
	if !plan.ApplyStartedAt.IsZero() {
		if plan.ApplyIdempotencyKey == match.IdempotencyKey && plan.ApplyAuthorizationFingerprint == match.AuthorizationFingerprint {
			return plan, nil
		}
		return infraapp.PlanRecord{}, infraapp.ErrConflict
	}
	if plan.Summary.PlanHash != a.PlanHash || plan.Estimate.Version != a.EstimateVersion || plan.Reservation.ReservationID != a.ReservationID || plan.Target != a.Target || !plan.Reservation.ExpiresAt.After(match.Now) || plan.Reservation.PlanHash != a.PlanHash {
		return infraapp.PlanRecord{}, infraapp.ErrPermissionDenied
	}
	if match.ApprovalRequired {
		grant, ok := s.approvals[a.ApprovalGrantID]
		if !ok || grant.TenantID != match.TenantID || grant.ProjectID != match.ProjectID || grant.PlanID != a.PlanID || grant.PlanHash != a.PlanHash || grant.EstimateVersion != a.EstimateVersion || grant.ReservationID != a.ReservationID || grant.Target != a.Target || grant.ActorID != a.ActorID || !grant.ExpiresAt.After(match.Now) || !grant.ConsumedAt.IsZero() {
			return infraapp.PlanRecord{}, infraapp.ErrPermissionDenied
		}
		grant.ConsumedAt = match.Now
		s.approvals[grant.GrantID] = grant
	}
	plan.ApplyStartedAt = match.Now
	plan.ApplyIdempotencyKey = match.IdempotencyKey
	plan.ApplyAuthorizationFingerprint = match.AuthorizationFingerprint
	plan.Version++
	s.plans[a.PlanID] = plan
	return plan, nil
}
