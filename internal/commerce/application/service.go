package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"sort"
	"strings"
	"time"

	"github.com/keir-research/ai-native-paas/internal/commerce/domain"
	commercev1 "github.com/keir-research/ai-native-paas/pkg/contracts/commerce/v1"
	commercev2 "github.com/keir-research/ai-native-paas/pkg/contracts/commerce/v2"
)

type CreatePlanDefinitionCommand struct{ ID, Name string }
type CreatePlanVersionCommand struct {
	ID            string
	DefinitionID  string
	PolicyVersion string
	Number        int64
	Spec          commercev1.PlanSpec
	EffectiveFrom time.Time
}
type StartSubscriptionCommand struct {
	ID            string
	TenantID      string
	PlanVersionID string
	PeriodID      string
	State         domain.SubscriptionState
	TrialEndsAt   time.Time
	PeriodStart   time.Time
	PeriodEnd     time.Time
}
type ReconcileUsageCommand struct {
	TenantID         string
	PeriodID         string
	ResourceType     string
	ResourceID       string
	Meter            commercev1.Meter
	WindowStart      time.Time
	WindowEnd        time.Time
	ObservedQuantity int64
}

type ObservedResourceAllocation struct {
	ExternalIdentity    string
	ResourceType        string
	Meter               commercev1.Meter
	UsageQuantity       int64
	ReservationQuantity int64
	WindowStart         time.Time
	WindowEnd           time.Time
	Metadata            map[string]string
}

type SettleApplyReservationCommand struct {
	TenantID       string
	ProjectID      string
	OperationID    string
	ReservationID  string
	IdempotencyKey string
	ObservedAt     time.Time
	Resources      []ObservedResourceAllocation
}

type ApplySettlementResult struct {
	Settlement  commercev2.ReservationSettlement
	Reservation commercev1.QuotaReservation
}

type ProviderUsageReport struct {
	TenantID        string
	ProjectID       string
	OperationID     string
	Provider        string
	ProviderEventID string
	PeriodID        string
	ResourceType    string
	ResourceID      string
	Meter           commercev1.Meter
	Quantity        int64
	OccurredAt      time.Time
	WindowStart     time.Time
	WindowEnd       time.Time
	Metadata        map[string]string
}

func (s *Service) CreatePlanDefinition(ctx context.Context, cmd CreatePlanDefinitionCommand) (domain.PlanDefinition, error) {
	if err := s.require(); err != nil {
		return domain.PlanDefinition{}, err
	}
	v, err := domain.NewPlanDefinition(cmd.ID, cmd.Name, s.now())
	if err != nil {
		return domain.PlanDefinition{}, err
	}
	err = s.Store.Transact(ctx, func(tx Tx) error {
		if err := tx.InsertPlanDefinition(v); err != nil {
			return err
		}
		return s.record(tx, "platform", "commerce.plan_definition.created", v.ID, v)
	})
	return v, err
}
func (s *Service) CreatePlanVersion(ctx context.Context, cmd CreatePlanVersionCommand) (domain.PlanVersion, error) {
	if err := s.require(); err != nil {
		return domain.PlanVersion{}, err
	}
	v, err := domain.NewPlanVersion(cmd.ID, cmd.DefinitionID, cmd.PolicyVersion, cmd.Number, cmd.Spec, cmd.EffectiveFrom, s.now())
	if err != nil {
		return domain.PlanVersion{}, err
	}
	err = s.Store.Transact(ctx, func(tx Tx) error {
		if _, ok := tx.GetPlanDefinition(v.DefinitionID); !ok {
			return domain.NewError(domain.CodeNotFound, "plan definition not found")
		}
		for _, existing := range tx.ListPlanVersions(v.DefinitionID) {
			if existing.Number == v.Number {
				return domain.NewError(domain.CodeConflict, "plan version number exists")
			}
		}
		if err := tx.InsertPlanVersion(v); err != nil {
			return err
		}
		return s.record(tx, "platform", "commerce.plan_version.created", v.ID, v)
	})
	return v, err
}
func (s *Service) ActivatePlanVersion(ctx context.Context, id string) (domain.PlanVersion, error) {
	return s.transitionPlan(ctx, id, "activate")
}
func (s *Service) DisablePlanVersion(ctx context.Context, id string) (domain.PlanVersion, error) {
	return s.transitionPlan(ctx, id, "disable")
}
func (s *Service) transitionPlan(ctx context.Context, id, action string) (domain.PlanVersion, error) {
	if err := s.require(); err != nil {
		return domain.PlanVersion{}, err
	}
	var result domain.PlanVersion
	err := s.Store.Transact(ctx, func(tx Tx) error {
		v, ok := tx.GetPlanVersion(strings.TrimSpace(id))
		if !ok {
			return domain.NewError(domain.CodeNotFound, "plan version not found")
		}
		old := v.Version
		var err error
		if action == "activate" {
			err = v.Activate(s.now())
		} else {
			err = v.Disable(s.now())
		}
		if err != nil {
			return err
		}
		if err = tx.UpdatePlanVersion(v, old); err != nil {
			return err
		}
		result = v
		return s.record(tx, "platform", "commerce.plan_version."+action, v.ID, v)
	})
	return result, err
}
func (s *Service) ReplacePlanSpec(ctx context.Context, id string, spec commercev1.PlanSpec) (domain.PlanVersion, error) {
	if err := s.require(); err != nil {
		return domain.PlanVersion{}, err
	}
	var result domain.PlanVersion
	err := s.Store.Transact(ctx, func(tx Tx) error {
		v, ok := tx.GetPlanVersion(strings.TrimSpace(id))
		if !ok {
			return domain.NewError(domain.CodeNotFound, "plan version not found")
		}
		old := v.Version
		if err := v.ReplaceSpec(spec, s.now()); err != nil {
			return err
		}
		if err := tx.UpdatePlanVersion(v, old); err != nil {
			return err
		}
		result = v
		return s.record(tx, "platform", "commerce.plan_version.spec_replaced", v.ID, map[string]string{"plan_version_id": v.ID})
	})
	return result, err
}
func (s *Service) StartSubscription(ctx context.Context, cmd StartSubscriptionCommand) (domain.Subscription, domain.BillingPeriod, error) {
	if err := s.require(); err != nil {
		return domain.Subscription{}, domain.BillingPeriod{}, err
	}
	now := s.now()
	sub, err := domain.NewSubscription(cmd.ID, cmd.TenantID, cmd.PlanVersionID, cmd.State, cmd.TrialEndsAt, now)
	if err != nil {
		return domain.Subscription{}, domain.BillingPeriod{}, err
	}
	period, err := domain.NewBillingPeriod(cmd.PeriodID, cmd.TenantID, cmd.ID, cmd.PlanVersionID, cmd.PeriodStart, cmd.PeriodEnd, now)
	if err != nil {
		return domain.Subscription{}, domain.BillingPeriod{}, err
	}
	err = s.Store.Transact(ctx, func(tx Tx) error {
		pv, ok := tx.GetPlanVersion(cmd.PlanVersionID)
		if !ok {
			return domain.NewError(domain.CodeNotFound, "plan version not found")
		}
		if pv.State != domain.PlanActive {
			return domain.NewError(domain.CodeConflict, "plan version is not active")
		}
		if existing, ok := tx.FindSubscriptionByTenant(cmd.TenantID); ok && existing.State != domain.SubscriptionCanceled {
			return domain.NewError(domain.CodeConflict, "tenant already has a subscription")
		}
		if err := tx.InsertSubscription(sub); err != nil {
			return err
		}
		if err := tx.InsertBillingPeriod(period); err != nil {
			return err
		}
		account, err := domain.NewCommercialAccount(cmd.TenantID, commercev1.CommercialActive, now)
		if err != nil {
			return err
		}
		if err = tx.InsertCommercialAccount(account); err != nil {
			return err
		}
		return s.record(tx, cmd.TenantID, "commerce.subscription.started", sub.ID, map[string]string{"subscription_id": sub.ID, "period_id": period.ID, "plan_version_id": pv.ID})
	})
	return sub, period, err
}

func (s *Service) CheckAs(ctx context.Context, actorTenant string, req commercev1.EntitlementRequest) (commercev1.EntitlementDecision, error) {
	if strings.TrimSpace(actorTenant) == "" || actorTenant != req.TenantID {
		return commercev1.EntitlementDecision{}, domain.NewError(domain.CodePermissionDenied, "cross-tenant entitlement lookup denied")
	}
	return s.Check(ctx, req)
}
func (s *Service) Check(ctx context.Context, req commercev1.EntitlementRequest) (commercev1.EntitlementDecision, error) {
	if err := s.require(); err != nil {
		return commercev1.EntitlementDecision{}, err
	}
	req.TenantID, req.Feature, req.Resource = strings.TrimSpace(req.TenantID), strings.TrimSpace(req.Feature), strings.TrimSpace(req.Resource)
	if req.TenantID == "" || req.Feature == "" || req.Quantity < 0 {
		return commercev1.EntitlementDecision{}, domain.NewError(domain.CodeInvalidArgument, "entitlement request is invalid")
	}
	at := req.At.UTC()
	if at.IsZero() {
		at = s.now()
	}
	var decision commercev1.EntitlementDecision
	err := s.Store.Transact(ctx, func(tx Tx) error {
		sub, ok := tx.FindSubscriptionByTenant(req.TenantID)
		if !ok {
			return domain.NewError(domain.CodeNotFound, "subscription not found")
		}
		pv, ok := tx.GetPlanVersion(sub.PlanVersionID)
		if !ok {
			return domain.NewError(domain.CodeUnavailable, "subscription plan is unavailable")
		}
		decision.PlanVersionID = pv.ID
		decision.PolicyVersion = pv.PolicyVersion
		account, ok := tx.GetCommercialAccount(req.TenantID)
		if !ok {
			return domain.NewError(domain.CodeUnavailable, "commercial account is unavailable")
		}
		if account.State == commercev1.CommercialSuspended || account.State == commercev1.CommercialCanceled || !sub.AllowsAllocation(at) {
			decision.Allowed = false
			decision.Reason = "commercial account does not allow allocation"
			return nil
		}
		if !pv.Spec.Features[req.Feature] {
			decision.Allowed = false
			decision.Reason = "feature is not included in the active plan"
			return nil
		}
		decision.Allowed = true
		decision.Reason = "feature is included in the active plan"
		if req.Resource != "" {
			limit, exists := pv.Spec.Quotas[req.Resource]
			if !exists {
				decision.Allowed = false
				decision.Reason = "resource quota is not defined"
				return nil
			}
			used := activeQuota(tx, req.TenantID, req.Resource, "", at)
			decision.Limit = limit
			decision.Remaining = max64(0, limit-used)
			if req.Quantity > decision.Remaining {
				decision.Allowed = false
				decision.Reason = "resource quota would be exceeded"
			}
		}
		return nil
	})
	return decision, err
}

func (s *Service) Reserve(ctx context.Context, req commercev1.QuotaRequest) (commercev1.QuotaReservation, error) {
	if err := s.require(); err != nil {
		return commercev1.QuotaReservation{}, err
	}
	req.TenantID, req.ProjectID, req.Resource, req.IdempotencyKey = strings.TrimSpace(req.TenantID), strings.TrimSpace(req.ProjectID), strings.TrimSpace(req.Resource), strings.TrimSpace(req.IdempotencyKey)
	if req.TenantID == "" || req.Resource == "" || req.IdempotencyKey == "" || req.Quantity <= 0 {
		return commercev1.QuotaReservation{}, domain.NewError(domain.CodeInvalidArgument, "quota request is invalid")
	}
	at := req.At.UTC()
	if at.IsZero() {
		at = s.now()
	}
	if !req.ExpiresAt.After(at) {
		return commercev1.QuotaReservation{}, domain.NewError(domain.CodeInvalidArgument, "quota reservation expiry is invalid")
	}
	fingerprint := fingerprint(req)
	var result commercev1.QuotaReservation
	err := s.Store.Transact(ctx, func(tx Tx) error {
		if existing, ok := tx.FindQuotaReservation(req.TenantID, req.IdempotencyKey); ok {
			if existing.Fingerprint != fingerprint {
				return domain.NewError(domain.CodeConflict, "quota idempotency payload mismatch")
			}
			result = existing.Contract()
			return nil
		}
		sub, ok := tx.FindSubscriptionByTenant(req.TenantID)
		if !ok {
			return domain.NewError(domain.CodeNotFound, "subscription not found")
		}
		account, ok := tx.GetCommercialAccount(req.TenantID)
		if !ok {
			return domain.NewError(domain.CodeUnavailable, "commercial account unavailable")
		}
		if !sub.AllowsAllocation(at) || account.State == commercev1.CommercialSuspended || account.State == commercev1.CommercialCanceled {
			return domain.NewError(domain.CodePermissionDenied, "commercial state denies allocation")
		}
		pv, ok := tx.GetPlanVersion(sub.PlanVersionID)
		if !ok {
			return domain.NewError(domain.CodeUnavailable, "plan version unavailable")
		}
		limit, ok := pv.Spec.Quotas[req.Resource]
		if !ok {
			return domain.NewError(domain.CodePermissionDenied, "resource is not included in plan")
		}
		used := activeQuota(tx, req.TenantID, req.Resource, req.ProjectID, at)
		if req.Quantity > limit-used {
			return domain.NewError(domain.CodeQuotaExceeded, "quota exceeded")
		}
		q, err := domain.NewQuotaReservation(s.newID("quota"), req.TenantID, req.ProjectID, req.Resource, pv.PolicyVersion, req.IdempotencyKey, fingerprint, req.Quantity, req.ExpiresAt, at)
		if err != nil {
			return err
		}
		if err = tx.InsertQuotaReservation(q); err != nil {
			return err
		}
		if err = s.record(tx, req.TenantID, "commerce.quota.reserved", q.ID, q.Contract()); err != nil {
			return err
		}
		result = q.Contract()
		return nil
	})
	return result, err
}
func activeQuota(tx Tx, tenant, resource, projectID string, at time.Time) int64 {
	var total int64
	for _, q := range tx.ListQuotaReservations(tenant, resource) {
		if projectID != "" && q.ProjectID != projectID {
			continue
		}
		quantity := q.QuantityAgainstLimit(at)
		if quantity > math.MaxInt64-total {
			return math.MaxInt64
		}
		total += quantity
	}
	return total
}

func (s *Service) SettleApplyReservation(ctx context.Context, cmd SettleApplyReservationCommand) (ApplySettlementResult, error) {
	if err := s.require(); err != nil {
		return ApplySettlementResult{}, err
	}
	cmd.TenantID = strings.TrimSpace(cmd.TenantID)
	cmd.ProjectID = strings.TrimSpace(cmd.ProjectID)
	cmd.OperationID = strings.TrimSpace(cmd.OperationID)
	cmd.ReservationID = strings.TrimSpace(cmd.ReservationID)
	cmd.IdempotencyKey = strings.TrimSpace(cmd.IdempotencyKey)
	cmd.ObservedAt = cmd.ObservedAt.UTC()
	if cmd.TenantID == "" || cmd.ProjectID == "" || cmd.OperationID == "" || cmd.ReservationID == "" || cmd.IdempotencyKey == "" || cmd.ObservedAt.IsZero() {
		return ApplySettlementResult{}, domain.NewError(domain.CodeInvalidArgument, "apply settlement identity is invalid")
	}
	resources := append([]ObservedResourceAllocation(nil), cmd.Resources...)
	seen := make(map[string]struct{}, len(resources))
	for index := range resources {
		item := &resources[index]
		item.ExternalIdentity = strings.TrimSpace(item.ExternalIdentity)
		item.ResourceType = strings.TrimSpace(item.ResourceType)
		item.WindowStart = item.WindowStart.UTC()
		item.WindowEnd = item.WindowEnd.UTC()
		item.Metadata = cloneStringMap(item.Metadata)
		if len(item.Metadata) == 0 {
			item.Metadata = nil
		}
		identity := item.ExternalIdentity + "\x00" + string(item.Meter)
		if item.ExternalIdentity == "" || item.ResourceType == "" || !commercev1.ValidMeter(item.Meter) || item.UsageQuantity <= 0 || item.ReservationQuantity <= 0 || !item.WindowStart.Before(item.WindowEnd) {
			return ApplySettlementResult{}, domain.NewError(domain.CodeInvalidArgument, "observed resource allocation is invalid")
		}
		if _, duplicate := seen[identity]; duplicate {
			return ApplySettlementResult{}, domain.NewError(domain.CodeInvalidArgument, "observed resource allocation is duplicated")
		}
		seen[identity] = struct{}{}
		owned, err := s.ownsProject(ctx, cmd.TenantID, cmd.ProjectID, item.ResourceType, item.ExternalIdentity)
		if err != nil {
			return ApplySettlementResult{}, domain.Wrap(domain.CodeUnavailable, "resource ownership unavailable", err)
		}
		if !owned {
			return ApplySettlementResult{}, domain.NewError(domain.CodePermissionDenied, "observed resource is not owned by tenant")
		}
	}
	sort.Slice(resources, func(i, j int) bool {
		return resources[i].ExternalIdentity+"\x00"+string(resources[i].Meter) < resources[j].ExternalIdentity+"\x00"+string(resources[j].Meter)
	})
	fingerprintValue := fingerprint(struct {
		TenantID      string
		ProjectID     string
		OperationID   string
		ReservationID string
		ObservedAt    time.Time
		Resources     []ObservedResourceAllocation
	}{cmd.TenantID, cmd.ProjectID, cmd.OperationID, cmd.ReservationID, cmd.ObservedAt, resources})
	var result ApplySettlementResult
	err := s.Store.Transact(ctx, func(tx Tx) error {
		reservation, ok := tx.GetQuotaReservation(cmd.ReservationID)
		if !ok {
			return domain.NewError(domain.CodeNotFound, "quota reservation not found")
		}
		if reservation.TenantID != cmd.TenantID {
			return domain.NewError(domain.CodePermissionDenied, "quota reservation belongs to another tenant")
		}
		if reservation.ProjectID == "" || reservation.ProjectID != cmd.ProjectID {
			return domain.NewError(domain.CodePermissionDenied, "quota reservation belongs to another project")
		}
		if existing, ok := tx.GetIdempotency(cmd.TenantID, "apply-settlement", cmd.IdempotencyKey); ok {
			if existing.Fingerprint != fingerprintValue {
				return domain.NewError(domain.CodeConflict, "apply settlement idempotency payload mismatch")
			}
			result = applySettlementResult(existing.ResourceID, cmd, reservation, len(resources))
			return nil
		}
		if reservation.State == domain.QuotaSettled {
			return domain.NewError(domain.CodeConflict, "quota reservation already settled")
		}
		settledQuantity := int64(0)
		for _, item := range resources {
			next, ok := safeAdd(settledQuantity, item.ReservationQuantity)
			if !ok || next > reservation.Quantity {
				return domain.NewError(domain.CodeInvalidArgument, "observed allocation exceeds reservation")
			}
			settledQuantity = next
		}
		for index, item := range resources {
			metadata := cloneStringMap(item.Metadata)
			if metadata == nil {
				metadata = map[string]string{}
			}
			metadata["project_id"] = cmd.ProjectID
			metadata["operation_id"] = cmd.OperationID
			metadata["reservation_id"] = cmd.ReservationID
			metadata["source"] = "provider_discovery"
			event := commercev1.UsageEvent{
				TenantID: cmd.TenantID, ResourceType: item.ResourceType, ResourceID: item.ExternalIdentity,
				Meter: item.Meter, Kind: commercev1.UsageStandard, Quantity: item.UsageQuantity,
				IdempotencyKey: fmt.Sprintf("%s:resource:%d", cmd.IdempotencyKey, index),
				OccurredAt:     cmd.ObservedAt, WindowStart: item.WindowStart, WindowEnd: item.WindowEnd, Metadata: metadata,
			}
			if err := s.appendUsageTx(tx, event); err != nil {
				return err
			}
		}
		oldVersion := reservation.Version
		if err := reservation.Settle(settledQuantity, cmd.ObservedAt); err != nil {
			return err
		}
		if err := tx.UpdateQuotaReservation(reservation, oldVersion); err != nil {
			return err
		}
		settlementID := s.newID("settlement")
		if err := tx.InsertIdempotency(IdempotencyRecord{TenantID: cmd.TenantID, Scope: "apply-settlement", Key: cmd.IdempotencyKey, Fingerprint: fingerprintValue, ResourceID: settlementID, CreatedAt: s.now()}); err != nil {
			return err
		}
		result = applySettlementResult(settlementID, cmd, reservation, len(resources))
		return s.record(tx, cmd.TenantID, "commerce.apply.settled", settlementID, result.Settlement)
	})
	if err != nil {
		return ApplySettlementResult{}, err
	}
	if err := result.Settlement.Validate(); err != nil {
		return ApplySettlementResult{}, domain.Wrap(domain.CodeInternal, "invalid apply settlement result", err)
	}
	return result, nil
}

func applySettlementResult(settlementID string, cmd SettleApplyReservationCommand, reservation domain.QuotaReservation, observed int) ApplySettlementResult {
	state := commercev2.SettlementApplied
	recoverable := false
	if reservation.ReleasedQuantity > 0 {
		state = commercev2.SettlementPartialRecoverable
		recoverable = true
	}
	return ApplySettlementResult{
		Settlement: commercev2.ReservationSettlement{
			SettlementID: settlementID, ReservationID: reservation.ID, ProjectID: cmd.ProjectID, Resource: reservation.Resource, OperationID: cmd.OperationID,
			SettledQuantity: reservation.SettledQuantity, ReleasedQuantity: reservation.ReleasedQuantity,
			ObservedResourceCount: int64(observed), State: state, Recoverable: recoverable, SettledAt: reservation.UpdatedAt,
		},
		Reservation: reservation.Contract(),
	}
}

func (s *Service) IngestProviderUsage(ctx context.Context, report ProviderUsageReport) (commercev2.UsageFact, error) {
	if err := s.require(); err != nil {
		return commercev2.UsageFact{}, err
	}
	report.TenantID = strings.TrimSpace(report.TenantID)
	report.ProjectID = strings.TrimSpace(report.ProjectID)
	report.OperationID = strings.TrimSpace(report.OperationID)
	report.Provider = strings.TrimSpace(report.Provider)
	report.ProviderEventID = strings.TrimSpace(report.ProviderEventID)
	report.PeriodID = strings.TrimSpace(report.PeriodID)
	report.ResourceType = strings.TrimSpace(report.ResourceType)
	report.ResourceID = strings.TrimSpace(report.ResourceID)
	report.OccurredAt = report.OccurredAt.UTC()
	report.WindowStart = report.WindowStart.UTC()
	report.WindowEnd = report.WindowEnd.UTC()
	report.Metadata = cloneStringMap(report.Metadata)
	if report.TenantID == "" || report.ProjectID == "" || report.OperationID == "" || report.Provider == "" || report.ProviderEventID == "" || report.ResourceType == "" || report.ResourceID == "" || !commercev1.ValidMeter(report.Meter) || report.Quantity <= 0 || report.OccurredAt.IsZero() || !report.WindowStart.Before(report.WindowEnd) {
		return commercev2.UsageFact{}, domain.NewError(domain.CodeInvalidArgument, "provider usage report is invalid")
	}
	owned, err := s.ownsProject(ctx, report.TenantID, report.ProjectID, report.ResourceType, report.ResourceID)
	if err != nil {
		return commercev2.UsageFact{}, domain.Wrap(domain.CodeUnavailable, "project resource ownership unavailable", err)
	}
	if !owned {
		return commercev2.UsageFact{}, domain.NewError(domain.CodePermissionDenied, "provider usage resource is not owned by project")
	}
	deduplicationKey := "provider-report:" + fingerprint(struct {
		Provider string
		EventID  string
	}{report.Provider, report.ProviderEventID})
	metadata := report.Metadata
	if metadata == nil {
		metadata = map[string]string{}
	}
	metadata["project_id"] = report.ProjectID
	metadata["operation_id"] = report.OperationID
	metadata["provider"] = report.Provider
	metadata["provider_event_id"] = report.ProviderEventID
	event, err := s.normalizeUsage(ctx, commercev1.UsageEvent{
		TenantID: report.TenantID, PeriodID: report.PeriodID, ResourceType: report.ResourceType, ResourceID: report.ResourceID,
		Meter: report.Meter, Kind: commercev1.UsageStandard, Quantity: report.Quantity, IdempotencyKey: deduplicationKey,
		OccurredAt: report.OccurredAt, WindowStart: report.WindowStart, WindowEnd: report.WindowEnd, Metadata: metadata,
	})
	if err != nil {
		return commercev2.UsageFact{}, err
	}
	var stored commercev1.UsageEvent
	err = s.Store.Transact(ctx, func(tx Tx) error {
		if err := s.appendUsageTx(tx, event); err != nil {
			return err
		}
		var ok bool
		stored, ok = tx.FindUsageByKey(report.TenantID, deduplicationKey)
		if !ok {
			return domain.NewError(domain.CodeInternal, "provider usage fact was not persisted")
		}
		return nil
	})
	if err != nil {
		return commercev2.UsageFact{}, err
	}
	fact := commercev2.UsageFact{
		UsageID: stored.ID, ProjectID: report.ProjectID, OperationID: report.OperationID,
		Provider: report.Provider, ProviderEventID: report.ProviderEventID, Meter: string(stored.Meter),
		Quantity: stored.Quantity, DeduplicationKey: stored.IdempotencyKey, OccurredAt: stored.OccurredAt,
	}
	if err := fact.Validate(); err != nil {
		return commercev2.UsageFact{}, domain.Wrap(domain.CodeInternal, "invalid provider usage fact", err)
	}
	return fact, nil
}
func (s *Service) Commit(ctx context.Context, id string) error {
	return s.transitionQuota(ctx, id, true)
}
func (s *Service) Release(ctx context.Context, id string) error {
	return s.transitionQuota(ctx, id, false)
}
func (s *Service) transitionQuota(ctx context.Context, id string, commit bool) error {
	if err := s.require(); err != nil {
		return err
	}
	return s.Store.Transact(ctx, func(tx Tx) error {
		q, ok := tx.GetQuotaReservation(strings.TrimSpace(id))
		if !ok {
			return domain.NewError(domain.CodeNotFound, "quota reservation not found")
		}
		old := q.Version
		var err error
		if commit {
			err = q.Commit(s.now())
		} else {
			err = q.Release(s.now())
		}
		if err != nil {
			return err
		}
		if q.Version == old {
			return nil
		}
		if err = tx.UpdateQuotaReservation(q, old); err != nil {
			return err
		}
		action := "released"
		if commit {
			action = "committed"
		}
		return s.record(tx, q.TenantID, "commerce.quota."+action, q.ID, q.Contract())
	})
}

func (s *Service) Append(ctx context.Context, event commercev1.UsageEvent) error {
	if err := s.require(); err != nil {
		return err
	}
	normalized, err := s.normalizeUsage(ctx, event)
	if err != nil {
		return err
	}
	return s.Store.Transact(ctx, func(tx Tx) error { return s.appendUsageTx(tx, normalized) })
}
func (s *Service) normalizeUsage(ctx context.Context, event commercev1.UsageEvent) (commercev1.UsageEvent, error) {
	event.TenantID, event.PeriodID, event.ResourceType, event.ResourceID, event.IdempotencyKey = strings.TrimSpace(event.TenantID), strings.TrimSpace(event.PeriodID), strings.TrimSpace(event.ResourceType), strings.TrimSpace(event.ResourceID), strings.TrimSpace(event.IdempotencyKey)
	if event.Kind == "" {
		event.Kind = commercev1.UsageStandard
	}
	if event.TenantID == "" || event.ResourceType == "" || event.ResourceID == "" || event.IdempotencyKey == "" || !commercev1.ValidMeter(event.Meter) {
		return commercev1.UsageEvent{}, domain.NewError(domain.CodeInvalidArgument, "usage event is invalid")
	}
	if event.Kind != commercev1.UsageStandard && event.Kind != commercev1.UsageCredit && event.Kind != commercev1.UsageCorrection {
		return commercev1.UsageEvent{}, domain.NewError(domain.CodeInvalidArgument, "usage kind is invalid")
	}
	if event.Quantity < 0 && event.Kind == commercev1.UsageStandard {
		return commercev1.UsageEvent{}, domain.NewError(domain.CodeInvalidArgument, "negative standard usage is invalid")
	}
	if event.OccurredAt.IsZero() {
		event.OccurredAt = s.now()
	}
	event.OccurredAt = event.OccurredAt.UTC()
	event.WindowStart = event.WindowStart.UTC()
	event.WindowEnd = event.WindowEnd.UTC()
	if event.WindowStart.IsZero() || event.WindowEnd.IsZero() || !event.WindowStart.Before(event.WindowEnd) {
		return commercev1.UsageEvent{}, domain.NewError(domain.CodeInvalidArgument, "usage window is invalid")
	}
	owned, err := s.ownership().Owns(ctx, event.TenantID, event.ResourceType, event.ResourceID)
	if err != nil {
		return commercev1.UsageEvent{}, domain.Wrap(domain.CodeUnavailable, "resource ownership unavailable", err)
	}
	if !owned {
		return commercev1.UsageEvent{}, domain.NewError(domain.CodePermissionDenied, "resource is not owned by tenant")
	}
	event.Metadata = cloneStringMap(event.Metadata)
	return event, nil
}
func (s *Service) appendUsageTx(tx Tx, event commercev1.UsageEvent) error {
	if event.PeriodID == "" {
		period, ok := findPeriod(tx, event.TenantID, event.OccurredAt, event.WindowStart, event.WindowEnd)
		if !ok {
			return domain.NewError(domain.CodeInvalidArgument, "no open billing period accepts usage")
		}
		event.PeriodID = period.ID
	}
	period, ok := tx.GetBillingPeriod(event.PeriodID)
	if !ok {
		return domain.NewError(domain.CodeNotFound, "billing period not found")
	}
	if period.TenantID != event.TenantID {
		return domain.NewError(domain.CodePermissionDenied, "billing period belongs to another tenant")
	}
	if !period.AcceptsUsage(event.WindowStart, event.WindowEnd) || !period.Contains(event.OccurredAt) {
		return domain.NewError(domain.CodeInvalidArgument, "usage is outside billing period")
	}
	normalized := event
	normalized.ID = ""
	normalized.CreatedAt = time.Time{}
	normalized.Version = 0
	fp := fingerprint(normalized)
	if existing, ok := tx.GetIdempotency(event.TenantID, "usage", event.IdempotencyKey); ok {
		if existing.Fingerprint != fp {
			return domain.NewError(domain.CodeConflict, "usage idempotency payload mismatch")
		}
		return nil
	}
	event.ID = s.newID("usage")
	event.CreatedAt = s.now()
	event.Version = 1
	if err := tx.InsertUsage(event); err != nil {
		return err
	}
	if err := tx.InsertIdempotency(IdempotencyRecord{TenantID: event.TenantID, Scope: "usage", Key: event.IdempotencyKey, Fingerprint: fp, ResourceID: event.ID, CreatedAt: s.now()}); err != nil {
		return err
	}
	return s.record(tx, event.TenantID, "commerce.usage.appended", event.ID, event)
}
func findPeriod(tx Tx, tenant string, occurred, start, end time.Time) (domain.BillingPeriod, bool) {
	for _, p := range tx.ListBillingPeriods(tenant) {
		if p.AcceptsUsage(start, end) && p.Contains(occurred) {
			return p, true
		}
	}
	return domain.BillingPeriod{}, false
}

func (s *Service) RecordBuildUsage(ctx context.Context, in commercev1.BuildUsage) error {
	if err := s.require(); err != nil {
		return err
	}
	if in.Outcome == commercev1.BuildPlatformFailed || (in.Outcome == commercev1.BuildCanceled && !in.FinishedAt.After(in.StartedAt)) {
		return nil
	}
	if strings.TrimSpace(in.TenantID) == "" || strings.TrimSpace(in.BuildID) == "" || strings.TrimSpace(in.IdempotencyKey) == "" || in.FinishedAt.Before(in.StartedAt) || in.CPUSeconds < 0 || in.MemoryGiBSeconds < 0 || in.DockerVMSeconds < 0 {
		return domain.NewError(domain.CodeInvalidArgument, "build usage is invalid")
	}
	owned, err := s.ownership().Owns(ctx, in.TenantID, "build", in.BuildID)
	if err != nil {
		return domain.Wrap(domain.CodeUnavailable, "build ownership unavailable", err)
	}
	if !owned {
		return domain.NewError(domain.CodePermissionDenied, "build is not owned by tenant")
	}
	return s.Store.Transact(ctx, func(tx Tx) error {
		period, ok := findPeriod(tx, in.TenantID, in.StartedAt.UTC(), in.StartedAt.UTC(), in.FinishedAt.UTC())
		if !ok {
			return domain.NewError(domain.CodeInvalidArgument, "build is outside an open billing period")
		}
		pv, ok := tx.GetPlanVersion(period.PlanVersionID)
		if !ok {
			return domain.NewError(domain.CodeUnavailable, "billing plan unavailable")
		}
		if in.Outcome == commercev1.BuildUserFailed && !pv.Spec.ChargeUserBuildFailures {
			return nil
		}
		values := []struct {
			meter commercev1.Meter
			q     int64
		}{{commercev1.MeterBuildCPUSeconds, in.CPUSeconds}, {commercev1.MeterBuildMemoryGiBSeconds, in.MemoryGiBSeconds}, {commercev1.MeterBuildDockerVMSeconds, in.DockerVMSeconds}}
		for _, item := range values {
			if item.q == 0 {
				continue
			}
			event := commercev1.UsageEvent{TenantID: in.TenantID, PeriodID: period.ID, ResourceType: "build", ResourceID: in.BuildID, Meter: item.meter, Kind: commercev1.UsageStandard, Quantity: item.q, IdempotencyKey: in.IdempotencyKey + ":" + string(item.meter), OccurredAt: in.FinishedAt.UTC(), WindowStart: in.StartedAt.UTC(), WindowEnd: in.FinishedAt.UTC(), Metadata: map[string]string{"outcome": string(in.Outcome)}}
			if err := s.appendUsageTx(tx, event); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Service) PreviewInvoice(ctx context.Context, tenantID, periodID string) (commercev1.InvoicePreview, error) {
	if err := s.require(); err != nil {
		return commercev1.InvoicePreview{}, err
	}
	tenantID, periodID = strings.TrimSpace(tenantID), strings.TrimSpace(periodID)
	if tenantID == "" || periodID == "" {
		return commercev1.InvoicePreview{}, domain.NewError(domain.CodeInvalidArgument, "invoice preview identity is required")
	}
	var preview commercev1.InvoicePreview
	err := s.Store.Transact(ctx, func(tx Tx) error {
		p, ok := tx.GetBillingPeriod(periodID)
		if !ok {
			return domain.NewError(domain.CodeNotFound, "billing period not found")
		}
		if p.TenantID != tenantID {
			return domain.NewError(domain.CodePermissionDenied, "billing period belongs to another tenant")
		}
		pv, ok := tx.GetPlanVersion(p.PlanVersionID)
		if !ok {
			return domain.NewError(domain.CodeUnavailable, "plan version unavailable")
		}
		preview = commercev1.InvoicePreview{TenantID: tenantID, PeriodID: periodID, Currency: pv.Spec.Currency, PlanVersionID: pv.ID, PolicyVersion: pv.PolicyVersion}
		type key struct {
			resourceType, resourceID string
			meter                    commercev1.Meter
			kind                     commercev1.UsageKind
		}
		quantities := map[key]int64{}
		for _, e := range tx.ListUsage(tenantID, periodID) {
			k := key{e.ResourceType, e.ResourceID, e.Meter, e.Kind}
			next, ok := safeAdd(quantities[k], e.Quantity)
			if !ok {
				return domain.NewError(domain.CodeOverflow, "usage quantity overflow")
			}
			quantities[k] = next
		}
		keys := make([]key, 0, len(quantities))
		for k := range quantities {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool {
			a, b := keys[i], keys[j]
			if a.resourceType != b.resourceType {
				return a.resourceType < b.resourceType
			}
			if a.resourceID != b.resourceID {
				return a.resourceID < b.resourceID
			}
			if a.meter != b.meter {
				return a.meter < b.meter
			}
			return a.kind < b.kind
		})
		remainingIncluded := map[commercev1.Meter]int64{}
		for m, q := range pv.Spec.Included {
			remainingIncluded[m] = q
		}
		var total int64
		for _, k := range keys {
			q := quantities[k]
			billable := q
			if k.kind == commercev1.UsageStandard && q > 0 {
				allowance := remainingIncluded[k.meter]
				if allowance > 0 {
					used := min64(q, allowance)
					billable = q - used
					remainingIncluded[k.meter] -= used
				}
			}
			amount := int64(0)
			if price, ok := pv.Spec.Prices[k.meter]; ok {
				var err error
				amount, err = RateQuantity(billable, price)
				if err != nil {
					return err
				}
			}
			next, ok := safeAdd(total, amount)
			if !ok {
				return domain.NewError(domain.CodeOverflow, "invoice total overflow")
			}
			total = next
			preview.Lines = append(preview.Lines, commercev1.InvoiceLine{ResourceType: k.resourceType, ResourceID: k.resourceID, Meter: k.meter, Kind: k.kind, Quantity: q, BillableQuantity: billable, AmountMinorUnits: amount})
		}
		preview.TotalMinorUnits = total
		preview.Normalize()
		return nil
	})
	return preview, err
}

func RateQuantity(quantity int64, price commercev1.Price) (int64, error) {
	if err := price.Validate(); err != nil {
		return 0, domain.Wrap(domain.CodeInvalidArgument, "price is invalid", err)
	}
	n := new(big.Int).Mul(big.NewInt(quantity), big.NewInt(price.MinorUnits))
	negative := n.Sign() < 0
	if negative {
		n.Abs(n)
	}
	d := big.NewInt(price.PerQuantity)
	half := new(big.Int).Quo(new(big.Int).Set(d), big.NewInt(2))
	n.Add(n, half)
	n.Quo(n, d)
	if negative {
		n.Neg(n)
	}
	if !n.IsInt64() {
		return 0, domain.NewError(domain.CodeOverflow, "rated amount overflow")
	}
	return n.Int64(), nil
}

func CalculateRuntimeUnitSeconds(observations []commercev1.RuntimeObservation, start, end time.Time) int64 {
	start, end = start.UTC(), end.UTC()
	if !start.Before(end) {
		return 0
	}
	type indexed struct {
		o commercev1.RuntimeObservation
		i int
	}
	items := make([]indexed, 0, len(observations))
	for i, o := range observations {
		o.At = o.At.UTC()
		if o.Replicas < 0 {
			o.Replicas = 0
		}
		if o.UnitWeight < 0 {
			o.UnitWeight = 0
		}
		items = append(items, indexed{o, i})
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].o.At.Equal(items[j].o.At) {
			return items[i].i < items[j].i
		}
		return items[i].o.At.Before(items[j].o.At)
	})
	dedup := make([]commercev1.RuntimeObservation, 0, len(items))
	for _, item := range items {
		if len(dedup) > 0 && dedup[len(dedup)-1].At.Equal(item.o.At) {
			dedup[len(dedup)-1] = item.o
		} else {
			dedup = append(dedup, item.o)
		}
	}
	current := commercev1.RuntimeObservation{At: start}
	idx := 0
	for idx < len(dedup) && !dedup[idx].At.After(start) {
		current = dedup[idx]
		idx++
	}
	cursor := start
	total := big.NewInt(0)
	add := func(until time.Time) {
		if until.After(end) {
			until = end
		}
		if !until.After(cursor) {
			return
		}
		if !current.Suspended && current.Replicas > 0 && current.UnitWeight > 0 {
			seconds := int64(until.Sub(cursor) / time.Second)
			term := new(big.Int).Mul(big.NewInt(seconds), big.NewInt(current.Replicas))
			term.Mul(term, big.NewInt(current.UnitWeight))
			total.Add(total, term)
		}
		cursor = until
	}
	for ; idx < len(dedup) && dedup[idx].At.Before(end); idx++ {
		if dedup[idx].At.After(cursor) {
			add(dedup[idx].At)
		}
		current = dedup[idx]
	}
	add(end)
	if !total.IsInt64() {
		return math.MaxInt64
	}
	return total.Int64()
}

func (s *Service) SetGrace(ctx context.Context, tenant string) (commercev1.RuntimeIntent, error) {
	return s.transitionCommercial(ctx, tenant, commercev1.CommercialGrace, "retain")
}
func (s *Service) Suspend(ctx context.Context, tenant string) (commercev1.RuntimeIntent, error) {
	return s.transitionCommercial(ctx, tenant, commercev1.CommercialSuspended, "suspend")
}
func (s *Service) Resume(ctx context.Context, tenant string) (commercev1.RuntimeIntent, error) {
	return s.transitionCommercial(ctx, tenant, commercev1.CommercialActive, "resume")
}
func (s *Service) transitionCommercial(ctx context.Context, tenant string, to commercev1.CommercialState, action string) (commercev1.RuntimeIntent, error) {
	if err := s.require(); err != nil {
		return commercev1.RuntimeIntent{}, err
	}
	tenant = strings.TrimSpace(tenant)
	if tenant == "" {
		return commercev1.RuntimeIntent{}, domain.NewError(domain.CodeInvalidArgument, "tenant is required")
	}
	intent := commercev1.RuntimeIntent{TenantID: tenant, Action: action, RetainManagedServices: true, At: s.now()}
	err := s.Store.Transact(ctx, func(tx Tx) error {
		account, ok := tx.GetCommercialAccount(tenant)
		if !ok {
			return domain.NewError(domain.CodeNotFound, "commercial account not found")
		}
		if account.State == to {
			return nil
		}
		old := account.Version
		if err := account.Transition(to, s.now()); err != nil {
			return err
		}
		if err := tx.UpdateCommercialAccount(account, old); err != nil {
			return err
		}
		if sub, ok := tx.FindSubscriptionByTenant(tenant); ok {
			oldSub := sub.Version
			switch to {
			case commercev1.CommercialGrace:
				sub.State = domain.SubscriptionGrace
			case commercev1.CommercialSuspended:
				sub.State = domain.SubscriptionSuspended
			case commercev1.CommercialActive:
				sub.State = domain.SubscriptionActive
			}
			sub.Version++
			sub.UpdatedAt = s.now()
			if err := tx.UpdateSubscription(sub, oldSub); err != nil {
				return err
			}
		}
		return s.record(tx, tenant, "commerce.runtime_intent."+action, tenant, intent)
	})
	return intent, err
}

func (s *Service) ReconcileUsage(ctx context.Context, cmd ReconcileUsageCommand) (int64, error) {
	if err := s.require(); err != nil {
		return 0, err
	}
	cmd.TenantID, cmd.PeriodID, cmd.ResourceType, cmd.ResourceID = strings.TrimSpace(cmd.TenantID), strings.TrimSpace(cmd.PeriodID), strings.TrimSpace(cmd.ResourceType), strings.TrimSpace(cmd.ResourceID)
	cmd.WindowStart, cmd.WindowEnd = cmd.WindowStart.UTC(), cmd.WindowEnd.UTC()
	if cmd.TenantID == "" || cmd.PeriodID == "" || cmd.ResourceType == "" || cmd.ResourceID == "" || !commercev1.ValidMeter(cmd.Meter) || !cmd.WindowStart.Before(cmd.WindowEnd) {
		return 0, domain.NewError(domain.CodeInvalidArgument, "reconciliation command is invalid")
	}
	owned, err := s.ownership().Owns(ctx, cmd.TenantID, cmd.ResourceType, cmd.ResourceID)
	if err != nil {
		return 0, domain.Wrap(domain.CodeUnavailable, "resource ownership unavailable", err)
	}
	if !owned {
		return 0, domain.NewError(domain.CodePermissionDenied, "resource is not owned by tenant")
	}
	var drift int64
	err = s.Store.Transact(ctx, func(tx Tx) error {
		var ledger int64
		for _, e := range tx.ListUsage(cmd.TenantID, cmd.PeriodID) {
			if e.ResourceType == cmd.ResourceType && e.ResourceID == cmd.ResourceID && e.Meter == cmd.Meter && !e.WindowStart.Before(cmd.WindowStart) && !e.WindowEnd.After(cmd.WindowEnd) {
				next, ok := safeAdd(ledger, e.Quantity)
				if !ok {
					return domain.NewError(domain.CodeOverflow, "ledger quantity overflow")
				}
				ledger = next
			}
		}
		var ok bool
		drift, ok = safeSub(cmd.ObservedQuantity, ledger)
		if !ok {
			return domain.NewError(domain.CodeOverflow, "reconciliation drift overflow")
		}
		if drift == 0 {
			return nil
		}
		key := "reconcile:" + fingerprint(cmd)
		event := commercev1.UsageEvent{TenantID: cmd.TenantID, PeriodID: cmd.PeriodID, ResourceType: cmd.ResourceType, ResourceID: cmd.ResourceID, Meter: cmd.Meter, Kind: commercev1.UsageCorrection, Quantity: drift, IdempotencyKey: key, OccurredAt: cmd.WindowEnd, WindowStart: cmd.WindowStart, WindowEnd: cmd.WindowEnd, Metadata: map[string]string{"source": "allocation_reconciler"}}
		if err := s.appendUsageTx(tx, event); err != nil {
			return err
		}
		threshold := s.DriftAlertThreshold
		if threshold > 0 && abs64(drift) > threshold {
			alert := ReconciliationAlert{ID: s.newID("alert"), TenantID: cmd.TenantID, PeriodID: cmd.PeriodID, Meter: string(cmd.Meter), ResourceID: cmd.ResourceID, Reason: "usage drift exceeds threshold", Drift: drift, CreatedAt: s.now()}
			if err := tx.AppendAlert(alert); err != nil {
				return err
			}
		}
		return nil
	})
	return drift, err
}

func (s *Service) record(tx Tx, tenant, topic, aggregate string, payload any) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return domain.Wrap(domain.CodeInternal, "event serialization failed", err)
	}
	now := s.now()
	if err = tx.AppendOutbox(OutboxRecord{ID: s.newID("evt"), TenantID: tenant, Topic: topic, AggregateID: aggregate, Payload: raw, CreatedAt: now}); err != nil {
		return err
	}
	return tx.AppendAudit(AuditRecord{ID: s.newID("audit"), TenantID: tenant, ActorID: "system", Action: topic, ResourceType: "commerce", ResourceID: aggregate, Data: raw, CreatedAt: now})
}
func fingerprint(v any) string {
	raw, _ := json.Marshal(v)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func cloneStringMap(in map[string]string) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}
func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
func safeAdd(a, b int64) (int64, bool) {
	if b > 0 && a > math.MaxInt64-b {
		return 0, false
	}
	if b < 0 && a < math.MinInt64-b {
		return 0, false
	}
	return a + b, true
}
func safeSub(a, b int64) (int64, bool) {
	if b == math.MinInt64 {
		if a >= 0 {
			return 0, false
		}
		return a - b, true
	}
	return safeAdd(a, -b)
}
func abs64(v int64) int64 {
	if v == math.MinInt64 {
		return math.MaxInt64
	}
	if v < 0 {
		return -v
	}
	return v
}
