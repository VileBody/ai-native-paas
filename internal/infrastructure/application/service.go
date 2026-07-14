package application

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"regexp"
	"sort"
	"strings"
	"time"

	commercev2 "github.com/keir-research/ai-native-paas/pkg/contracts/commerce/v2"
	infrastructurev1 "github.com/keir-research/ai-native-paas/pkg/contracts/infrastructure/v1"
)

var sourceRevision = regexp.MustCompile(`^[0-9a-f]{40,64}$`)

type PlanCommand struct {
	TenantID        string
	ProjectID       string
	ActorID         string
	WorkspaceID     string
	Target          string
	SourceSHA       string
	IdempotencyKey  string
	ArtifactDigest  string
	StateGeneration int64
	PlanJSON        []byte
}

type PlanResult struct {
	Summary     infrastructurev1.PlanSummary    `json:"summary"`
	Estimate    commercev2.CostEstimate         `json:"estimate"`
	Reservation commercev2.ExecutionReservation `json:"reservation"`
}

type ReceiptPlanCommand struct {
	TenantID        string
	ProjectID       string
	ActorID         string
	WorkspaceID     string
	CommandID       string
	Target          string
	SourceSHA       string
	IdempotencyKey  string
	StateGeneration int64
}

func (s *Service) PlanFromReceipt(ctx context.Context, command ReceiptPlanCommand) (PlanResult, error) {
	if s == nil || s.Store == nil || command.CommandID == "" {
		return PlanResult{}, errors.New("infrastructure receipt service is unavailable")
	}
	receipt, err := s.Store.GetPlanReceipt(ctx, command.TenantID, command.ProjectID, command.CommandID)
	if errors.Is(err, ErrNotFound) {
		return PlanResult{}, ErrDependencyPending
	}
	if err != nil {
		return PlanResult{}, err
	}
	if receipt.WorkspaceID != command.WorkspaceID || receipt.ActorID != command.ActorID || receipt.ArtifactDigest == "" || len(receipt.PlanJSON) == 0 {
		return PlanResult{}, ErrPermissionDenied
	}
	return s.Plan(ctx, PlanCommand{
		TenantID: command.TenantID, ProjectID: command.ProjectID, ActorID: command.ActorID,
		WorkspaceID: command.WorkspaceID, Target: command.Target, SourceSHA: command.SourceSHA,
		IdempotencyKey: command.IdempotencyKey, ArtifactDigest: receipt.ArtifactDigest,
		StateGeneration: command.StateGeneration, PlanJSON: append([]byte(nil), receipt.PlanJSON...),
	})
}

type tofuPlan struct {
	ResourceChanges []struct {
		Address      string `json:"address"`
		ProviderName string `json:"provider_name"`
		Type         string `json:"type"`
		Change       struct {
			Actions []string `json:"actions"`
		} `json:"change"`
	} `json:"resource_changes"`
}

func (s *Service) Plan(ctx context.Context, command PlanCommand) (PlanResult, error) {
	if s == nil || s.Store == nil || s.Clock == nil || s.IDs == nil {
		return PlanResult{}, errors.New("infrastructure service is unavailable")
	}
	now := s.Clock.Now().UTC()
	if err := validatePlanCommand(command); err != nil {
		return PlanResult{}, err
	}
	if err := s.Prices.Validate(); err != nil {
		return PlanResult{}, fmt.Errorf("invalid price book: %w", err)
	}
	changes, destructive, err := normalizeChanges(command.PlanJSON)
	if err != nil {
		return PlanResult{}, err
	}
	planHash, err := hashJSON(struct {
		ProjectID       string                            `json:"project_id"`
		WorkspaceID     string                            `json:"workspace_id"`
		Target          string                            `json:"target"`
		SourceSHA       string                            `json:"source_sha"`
		ArtifactDigest  string                            `json:"artifact_digest"`
		StateGeneration int64                             `json:"state_generation"`
		Changes         []infrastructurev1.ResourceChange `json:"changes"`
	}{command.ProjectID, command.WorkspaceID, command.Target, command.SourceSHA, command.ArtifactDigest, command.StateGeneration, changes})
	if err != nil {
		return PlanResult{}, err
	}
	estimate, err := s.estimate(planHash, changes, now)
	if err != nil {
		return PlanResult{}, err
	}
	requiresApproval := destructive || strings.EqualFold(command.Target, "production") || estimate.ApprovalRequired
	estimate.ApprovalRequired = requiresApproval
	if err := estimate.Validate(now); err != nil {
		return PlanResult{}, err
	}
	estimateFingerprint, err := hashJSON(estimate)
	if err != nil {
		return PlanResult{}, err
	}
	planID := s.IDs.New("plan")
	reservationTTL := durationOr(s.ReservationTTL, 20*time.Minute)
	reservationExpiry := now.Add(reservationTTL)
	if estimate.ExpiresAt.Before(reservationExpiry) {
		reservationExpiry = estimate.ExpiresAt
	}
	reservation := commercev2.ExecutionReservation{
		ReservationID: s.IDs.New("reservation"), ProjectID: command.ProjectID,
		EstimateID: estimate.EstimateID, EstimateVersion: estimate.Version,
		PlanHash: planHash, Maximum: estimate.Maximum, ExpiresAt: reservationExpiry,
	}
	summary := infrastructurev1.PlanSummary{
		PlanRef: infrastructurev1.PlanRef{
			PlanID: planID, ProjectID: command.ProjectID, WorkspaceID: command.WorkspaceID,
			SourceSHA: command.SourceSHA, PlanHash: planHash,
			StateGeneration: command.StateGeneration, CreatedAt: now,
		},
		Changes: changes, Destructive: destructive, RequiresApproval: requiresApproval,
		EstimateVersion: estimate.Version, EstimateFingerprint: estimateFingerprint,
	}
	if err := summary.PlanRef.Validate(); err != nil {
		return PlanResult{}, err
	}
	fingerprint, err := hashJSON(struct {
		TenantID        string `json:"tenant_id"`
		ActorID         string `json:"actor_id"`
		IdempotencyKey  string `json:"idempotency_key"`
		PlanHash        string `json:"plan_hash"`
		RateCardID      string `json:"rate_card_id"`
		RateCardVersion string `json:"rate_card_version"`
	}{command.TenantID, command.ActorID, command.IdempotencyKey, planHash, s.Prices.RateCard.RateCardID, s.Prices.RateCard.Version})
	if err != nil {
		return PlanResult{}, err
	}
	record, err := s.Store.CreatePlan(ctx, PlanRecord{
		TenantID: command.TenantID, RequestedByActorID: command.ActorID, IdempotencyKey: command.IdempotencyKey,
		IdempotencyFingerprint: fingerprint, Summary: summary,
		ArtifactDigest: command.ArtifactDigest, Target: command.Target,
		Estimate: estimate, Reservation: reservation, Version: 1,
	})
	if err != nil {
		return PlanResult{}, err
	}
	return PlanResult{Summary: record.Summary, Estimate: record.Estimate, Reservation: record.Reservation}, nil
}

type GrantApprovalCommand struct {
	TenantID       string
	ProjectID      string
	PlanID         string
	ActorID        string
	ApproverUserID string
	ExpiresAt      time.Time
}

func (s *Service) GrantApproval(ctx context.Context, command GrantApprovalCommand) (ApprovalGrant, error) {
	if s == nil || s.Store == nil || s.Clock == nil || s.IDs == nil {
		return ApprovalGrant{}, errors.New("infrastructure service is unavailable")
	}
	now := s.Clock.Now().UTC()
	if command.TenantID == "" || command.ProjectID == "" || command.PlanID == "" || command.ApproverUserID == "" || !command.ExpiresAt.After(now) {
		return ApprovalGrant{}, errors.New("invalid approval grant command")
	}
	plan, err := s.Store.GetPlan(ctx, command.TenantID, command.ProjectID, command.PlanID)
	if err != nil {
		return ApprovalGrant{}, err
	}
	actorID := plan.RequestedByActorID
	if actorID == "" || (command.ActorID != "" && command.ActorID != actorID) {
		return ApprovalGrant{}, ErrPermissionDenied
	}
	expiresAt := command.ExpiresAt.UTC()
	if plan.Reservation.ExpiresAt.Before(expiresAt) {
		expiresAt = plan.Reservation.ExpiresAt
	}
	return s.Store.CreateApproval(ctx, ApprovalGrant{
		GrantID: s.IDs.New("approval"), TenantID: command.TenantID, ProjectID: command.ProjectID,
		PlanID: plan.Summary.PlanID, PlanHash: plan.Summary.PlanHash,
		EstimateVersion: plan.Estimate.Version, ReservationID: plan.Reservation.ReservationID,
		Target: plan.Target, ActorID: actorID, ApproverUserID: command.ApproverUserID,
		CreatedAt: now, ExpiresAt: expiresAt,
	})
}

type ApprovalStatus struct {
	PlanID     string    `json:"plan_id"`
	Status     string    `json:"status"`
	GrantID    string    `json:"approval_grant_id,omitempty"`
	ApproverID string    `json:"approver_user_id,omitempty"`
	ExpiresAt  time.Time `json:"expires_at,omitempty"`
}

func (s *Service) GetPlan(ctx context.Context, tenantID, projectID, planID string) (PlanRecord, error) {
	if s == nil || s.Store == nil || tenantID == "" || projectID == "" || planID == "" {
		return PlanRecord{}, errors.New("invalid infrastructure plan lookup")
	}
	return s.Store.GetPlan(ctx, tenantID, projectID, planID)
}

func (s *Service) GetApprovalStatus(ctx context.Context, tenantID, projectID, planID, actorID string) (ApprovalStatus, error) {
	if s == nil || s.Store == nil || s.Clock == nil || tenantID == "" || projectID == "" || planID == "" || actorID == "" {
		return ApprovalStatus{}, errors.New("invalid infrastructure approval lookup")
	}
	plan, err := s.Store.GetPlan(ctx, tenantID, projectID, planID)
	if err != nil {
		return ApprovalStatus{}, err
	}
	if plan.RequestedByActorID != actorID {
		return ApprovalStatus{}, ErrPermissionDenied
	}
	if !plan.ApplyStartedAt.IsZero() {
		return ApprovalStatus{PlanID: planID, Status: "CONSUMED"}, nil
	}
	if !plan.Summary.RequiresApproval {
		return ApprovalStatus{PlanID: planID, Status: "NOT_REQUIRED"}, nil
	}
	grant, err := s.Store.GetActiveApproval(ctx, tenantID, projectID, planID, actorID, s.Clock.Now().UTC())
	if errors.Is(err, ErrNotFound) {
		return ApprovalStatus{PlanID: planID, Status: "WAITING_APPROVAL"}, nil
	}
	if err != nil {
		return ApprovalStatus{}, err
	}
	return ApprovalStatus{PlanID: planID, Status: "APPROVED", GrantID: grant.GrantID, ApproverID: grant.ApproverUserID, ExpiresAt: grant.ExpiresAt}, nil
}

type ApplyCommand struct {
	TenantID       string
	ProjectID      string
	IdempotencyKey string
	Authorization  infrastructurev1.ApplyAuthorization
}

func (s *Service) AuthorizeApply(ctx context.Context, command ApplyCommand) (PlanRecord, error) {
	if s == nil || s.Store == nil || s.Clock == nil {
		return PlanRecord{}, errors.New("infrastructure service is unavailable")
	}
	now := s.Clock.Now().UTC()
	if strings.TrimSpace(command.IdempotencyKey) == "" || len(command.IdempotencyKey) > 128 {
		return PlanRecord{}, errors.New("invalid infrastructure apply idempotency key")
	}
	plan, err := s.Store.GetPlan(ctx, command.TenantID, command.ProjectID, command.Authorization.PlanID)
	if err != nil {
		return PlanRecord{}, err
	}
	validationTime := now
	if !plan.ApplyStartedAt.IsZero() {
		validationTime = plan.ApplyStartedAt.Add(-time.Nanosecond)
	}
	if err := command.Authorization.Validate(validationTime, plan.Summary.RequiresApproval); err != nil {
		if plan.Summary.RequiresApproval && command.Authorization.ApprovalGrantID == "" {
			return PlanRecord{}, ErrApprovalRequired
		}
		return PlanRecord{}, err
	}
	fingerprint, err := hashJSON(struct {
		TenantID       string                              `json:"tenant_id"`
		ProjectID      string                              `json:"project_id"`
		IdempotencyKey string                              `json:"idempotency_key"`
		Authorization  infrastructurev1.ApplyAuthorization `json:"authorization"`
	}{command.TenantID, command.ProjectID, command.IdempotencyKey, command.Authorization})
	if err != nil {
		return PlanRecord{}, err
	}
	return s.Store.AuthorizeApply(ctx, ApplyMatch{
		TenantID: command.TenantID, ProjectID: command.ProjectID,
		Authorization: command.Authorization, ApprovalRequired: plan.Summary.RequiresApproval, Now: now,
		IdempotencyKey: command.IdempotencyKey, AuthorizationFingerprint: fingerprint,
	})
}

func validatePlanCommand(command PlanCommand) error {
	if strings.TrimSpace(command.TenantID) == "" || strings.TrimSpace(command.ProjectID) == "" || strings.TrimSpace(command.ActorID) == "" || strings.TrimSpace(command.WorkspaceID) == "" || strings.TrimSpace(command.Target) == "" || strings.TrimSpace(command.IdempotencyKey) == "" || len(command.IdempotencyKey) > 128 || !sourceRevision.MatchString(command.SourceSHA) || !regexp.MustCompile(`^sha256:[0-9a-f]{64}$`).MatchString(command.ArtifactDigest) || command.StateGeneration < 0 || len(command.PlanJSON) == 0 || len(command.PlanJSON) > 8<<20 {
		return errors.New("invalid infrastructure plan command")
	}
	return nil
}

func normalizeChanges(raw []byte) ([]infrastructurev1.ResourceChange, bool, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var plan tofuPlan
	if err := decoder.Decode(&plan); err != nil {
		// OpenTofu adds top-level fields over time. Decode the same typed view
		// without rejecting fields that do not affect canonical change identity.
		if err := json.Unmarshal(raw, &plan); err != nil {
			return nil, false, errors.New("invalid OpenTofu plan JSON")
		}
	}
	changes := make([]infrastructurev1.ResourceChange, 0, len(plan.ResourceChanges))
	destructive := false
	for _, change := range plan.ResourceChanges {
		action, include, err := normalizeAction(change.Change.Actions)
		if err != nil || strings.TrimSpace(change.Address) == "" || strings.TrimSpace(change.Type) == "" {
			return nil, false, errors.New("invalid OpenTofu resource change")
		}
		if !include {
			continue
		}
		if action == infrastructurev1.ActionDelete || action == infrastructurev1.ActionReplace {
			destructive = true
		}
		changes = append(changes, infrastructurev1.ResourceChange{
			Address: change.Address, Provider: change.ProviderName,
			ResourceType: change.Type, Action: action,
		})
	}
	if len(changes) == 0 {
		changes = append(changes, infrastructurev1.ResourceChange{Address: "plan", ResourceType: "noop", Action: infrastructurev1.ActionNoOp})
	}
	sort.Slice(changes, func(i, j int) bool {
		a, b := changes[i], changes[j]
		return a.Address+"\x00"+a.Provider+"\x00"+a.ResourceType+"\x00"+string(a.Action) < b.Address+"\x00"+b.Provider+"\x00"+b.ResourceType+"\x00"+string(b.Action)
	})
	return changes, destructive, nil
}

func normalizeAction(actions []string) (infrastructurev1.ChangeAction, bool, error) {
	value := strings.Join(actions, ",")
	switch value {
	case "create":
		return infrastructurev1.ActionCreate, true, nil
	case "update":
		return infrastructurev1.ActionUpdate, true, nil
	case "delete,create", "create,delete":
		return infrastructurev1.ActionReplace, true, nil
	case "delete":
		return infrastructurev1.ActionDelete, true, nil
	case "no-op":
		return infrastructurev1.ActionNoOp, true, nil
	case "read":
		return "", false, nil
	default:
		return "", false, errors.New("unsupported OpenTofu action")
	}
}

func (s *Service) estimate(planHash string, changes []infrastructurev1.ResourceChange, now time.Time) (commercev2.CostEstimate, error) {
	lines := make([]commercev2.EstimateLine, 0, len(changes))
	var minimum, maximum int64
	unknown := false
	for _, change := range changes {
		price, exists := s.Prices.Prices[change.ResourceType]
		if change.Action == infrastructurev1.ActionDelete || change.Action == infrastructurev1.ActionNoOp {
			price = UnitPrice{Meter: change.ResourceType, Unit: "resource-month", Known: true}
			exists = true
		}
		if !exists {
			price = UnitPrice{Meter: change.ResourceType, Unit: "resource-month", UnknownMaximumMinor: 1_000_000, Known: false}
		}
		provider := price.ProviderMinorPerQuantity
		upper := provider
		if !price.Known {
			unknown = true
			upper = price.UnknownMaximumMinor
			if upper <= 0 {
				upper = 1_000_000
			}
		}
		customer, err := markup(upper, s.Prices.RateCard.MarkupBasisPoints)
		if err != nil || maximum > int64(^uint64(0)>>1)-customer {
			return commercev2.CostEstimate{}, errors.New("cost estimate overflow")
		}
		knownCustomer := int64(0)
		if price.Known {
			knownCustomer = customer
			minimum += customer
		}
		maximum += customer
		lines = append(lines, commercev2.EstimateLine{
			Meter: nonempty(price.Meter, change.ResourceType), Quantity: 1, Unit: nonempty(price.Unit, "resource-month"),
			ProviderCost: commercev2.Money{Currency: s.Prices.RateCard.Currency, MinorUnit: provider},
			CustomerCost: commercev2.Money{Currency: s.Prices.RateCard.Currency, MinorUnit: max64(knownCustomer, customer)},
			PriceKnown:   price.Known,
		})
	}
	ttl := durationOr(s.EstimateTTL, 30*time.Minute)
	versionHash, err := hashJSON(struct {
		RateCardID      string `json:"rate_card_id"`
		Version         string `json:"version"`
		PriceSnapshotID string `json:"price_snapshot_id"`
		PlanHash        string `json:"plan_hash"`
	}{s.Prices.RateCard.RateCardID, s.Prices.RateCard.Version, s.Prices.RateCard.PriceSnapshotID, planHash})
	if err != nil {
		return commercev2.CostEstimate{}, err
	}
	return commercev2.CostEstimate{
		EstimateID: s.IDs.New("estimate"), Version: versionHash,
		PlanHash: planHash, RateCardID: s.Prices.RateCard.RateCardID, Lines: lines,
		Minimum:          commercev2.Money{Currency: s.Prices.RateCard.Currency, MinorUnit: minimum},
		Maximum:          commercev2.Money{Currency: s.Prices.RateCard.Currency, MinorUnit: maximum},
		ApprovalRequired: unknown, ExpiresAt: now.Add(ttl),
	}, nil
}

func markup(providerMinor, basisPoints int64) (int64, error) {
	if providerMinor < 0 || basisPoints < 1000 || basisPoints > 5000 {
		return 0, errors.New("invalid markup input")
	}
	n := new(big.Int).Mul(big.NewInt(providerMinor), big.NewInt(10_000+basisPoints))
	n.Add(n, big.NewInt(9_999))
	n.Quo(n, big.NewInt(10_000))
	if !n.IsInt64() {
		return 0, errors.New("markup overflow")
	}
	return n.Int64(), nil
}

func hashJSON(value any) (string, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

func durationOr(value, fallback time.Duration) time.Duration {
	if value <= 0 {
		return fallback
	}
	return value
}

func nonempty(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
