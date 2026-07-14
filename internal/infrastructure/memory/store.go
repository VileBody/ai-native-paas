package memory

import (
	"sync"

	infraapp "github.com/keir-research/ai-native-paas/internal/infrastructure/application"
)

type Store struct {
	mu          sync.Mutex
	plans       map[string]infraapp.PlanRecord
	idempotency map[string]string
	approvals   map[string]infraapp.ApprovalGrant
}

func New() *Store {
	return &Store{plans: map[string]infraapp.PlanRecord{}, idempotency: map[string]string{}, approvals: map[string]infraapp.ApprovalGrant{}}
}

func (s *Store) CreatePlan(record infraapp.PlanRecord) (infraapp.PlanRecord, error) {
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

func (s *Store) GetPlan(tenantID, projectID, planID string) (infraapp.PlanRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.plans[planID]
	if !ok || record.TenantID != tenantID || record.Summary.ProjectID != projectID {
		return infraapp.PlanRecord{}, infraapp.ErrNotFound
	}
	return record, nil
}

func (s *Store) CreateApproval(grant infraapp.ApprovalGrant) (infraapp.ApprovalGrant, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.approvals[grant.GrantID]; ok {
		return infraapp.ApprovalGrant{}, infraapp.ErrConflict
	}
	plan, ok := s.plans[grant.PlanID]
	if !ok || plan.TenantID != grant.TenantID || plan.Summary.ProjectID != grant.ProjectID || !plan.Summary.RequiresApproval || !grant.ExpiresAt.After(plan.Summary.CreatedAt) {
		return infraapp.ApprovalGrant{}, infraapp.ErrPermissionDenied
	}
	s.approvals[grant.GrantID] = grant
	return grant, nil
}

func (s *Store) AuthorizeApply(match infraapp.ApplyMatch) (infraapp.PlanRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a := match.Authorization
	plan, ok := s.plans[a.PlanID]
	if !ok || plan.TenantID != match.TenantID || plan.Summary.ProjectID != match.ProjectID {
		return infraapp.PlanRecord{}, infraapp.ErrNotFound
	}
	if !plan.ApplyStartedAt.IsZero() {
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
	plan.Version++
	s.plans[a.PlanID] = plan
	return plan, nil
}
