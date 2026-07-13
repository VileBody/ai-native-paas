package domain

import (
	"strings"
	"time"

	commercev1 "github.com/keir-research/ai-native-paas/pkg/contracts/commerce/v1"
)

type PlanDefinition struct {
	ID        string
	Name      string
	Version   int64
	CreatedAt time.Time
	UpdatedAt time.Time
}

func NewPlanDefinition(id, name string, now time.Time) (PlanDefinition, error) {
	id, name = strings.TrimSpace(id), strings.TrimSpace(name)
	if id == "" || name == "" {
		return PlanDefinition{}, NewError(CodeInvalidArgument, "plan definition identity is required")
	}
	now = now.UTC()
	return PlanDefinition{ID: id, Name: name, Version: 1, CreatedAt: now, UpdatedAt: now}, nil
}

type PlanState string

const (
	PlanDraft    PlanState = "DRAFT"
	PlanActive   PlanState = "ACTIVE"
	PlanDisabled PlanState = "DISABLED"
)

type PlanVersion struct {
	ID            string
	DefinitionID  string
	PolicyVersion string
	Number        int64
	Spec          commercev1.PlanSpec
	State         PlanState
	EffectiveFrom time.Time
	Version       int64
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

func ClonePlanSpec(in commercev1.PlanSpec) commercev1.PlanSpec {
	out := in
	out.Features = cloneBoolMap(in.Features)
	out.Quotas = cloneIntMap(in.Quotas)
	out.Prices = clonePriceMap(in.Prices)
	out.Included = cloneMeterIntMap(in.Included)
	return out
}
func cloneBoolMap(in map[string]bool) map[string]bool {
	if in == nil {
		return nil
	}
	out := make(map[string]bool, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
func cloneIntMap(in map[string]int64) map[string]int64 {
	if in == nil {
		return nil
	}
	out := make(map[string]int64, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
func clonePriceMap(in map[commercev1.Meter]commercev1.Price) map[commercev1.Meter]commercev1.Price {
	if in == nil {
		return nil
	}
	out := make(map[commercev1.Meter]commercev1.Price, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
func cloneMeterIntMap(in map[commercev1.Meter]int64) map[commercev1.Meter]int64 {
	if in == nil {
		return nil
	}
	out := make(map[commercev1.Meter]int64, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func NewPlanVersion(id, definitionID, policyVersion string, number int64, spec commercev1.PlanSpec, effectiveFrom, now time.Time) (PlanVersion, error) {
	id, definitionID, policyVersion = strings.TrimSpace(id), strings.TrimSpace(definitionID), strings.TrimSpace(policyVersion)
	if id == "" || definitionID == "" || policyVersion == "" || number <= 0 {
		return PlanVersion{}, NewError(CodeInvalidArgument, "plan version identity is invalid")
	}
	if err := spec.Validate(); err != nil {
		return PlanVersion{}, Wrap(CodeInvalidArgument, "plan specification is invalid", err)
	}
	now = now.UTC()
	return PlanVersion{ID: id, DefinitionID: definitionID, PolicyVersion: policyVersion, Number: number, Spec: ClonePlanSpec(spec), State: PlanDraft, EffectiveFrom: effectiveFrom.UTC(), Version: 1, CreatedAt: now, UpdatedAt: now}, nil
}
func (p *PlanVersion) Activate(now time.Time) error {
	if p.State == PlanActive {
		return nil
	}
	if p.State != PlanDraft {
		return NewError(CodeConflict, "only draft plan versions can be activated")
	}
	p.State = PlanActive
	p.Version++
	p.UpdatedAt = now.UTC()
	return nil
}
func (p *PlanVersion) Disable(now time.Time) error {
	if p.State == PlanDisabled {
		return nil
	}
	if p.State != PlanDraft && p.State != PlanActive {
		return NewError(CodeConflict, "plan version cannot be disabled")
	}
	p.State = PlanDisabled
	p.Version++
	p.UpdatedAt = now.UTC()
	return nil
}
func (p *PlanVersion) ReplaceSpec(spec commercev1.PlanSpec, now time.Time) error {
	if p.State != PlanDraft {
		return NewError(CodeConflict, "active plan version is immutable")
	}
	if err := spec.Validate(); err != nil {
		return Wrap(CodeInvalidArgument, "plan specification is invalid", err)
	}
	p.Spec = ClonePlanSpec(spec)
	p.Version++
	p.UpdatedAt = now.UTC()
	return nil
}

type SubscriptionState string

const (
	SubscriptionTrial     SubscriptionState = "TRIAL"
	SubscriptionActive    SubscriptionState = "ACTIVE"
	SubscriptionGrace     SubscriptionState = "GRACE"
	SubscriptionSuspended SubscriptionState = "SUSPENDED"
	SubscriptionCanceled  SubscriptionState = "CANCELED"
)

type Subscription struct {
	ID            string
	TenantID      string
	PlanVersionID string
	State         SubscriptionState
	TrialEndsAt   time.Time
	Version       int64
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

func NewSubscription(id, tenantID, planVersionID string, state SubscriptionState, trialEndsAt, now time.Time) (Subscription, error) {
	id, tenantID, planVersionID = strings.TrimSpace(id), strings.TrimSpace(tenantID), strings.TrimSpace(planVersionID)
	if id == "" || tenantID == "" || planVersionID == "" {
		return Subscription{}, NewError(CodeInvalidArgument, "subscription identity is required")
	}
	if state == "" {
		state = SubscriptionActive
	}
	switch state {
	case SubscriptionTrial, SubscriptionActive, SubscriptionGrace, SubscriptionSuspended:
	default:
		return Subscription{}, NewError(CodeInvalidArgument, "invalid subscription state")
	}
	if state == SubscriptionTrial && trialEndsAt.IsZero() {
		return Subscription{}, NewError(CodeInvalidArgument, "trial end is required")
	}
	now = now.UTC()
	return Subscription{ID: id, TenantID: tenantID, PlanVersionID: planVersionID, State: state, TrialEndsAt: trialEndsAt.UTC(), Version: 1, CreatedAt: now, UpdatedAt: now}, nil
}
func (s Subscription) AllowsAllocation(at time.Time) bool {
	switch s.State {
	case SubscriptionActive, SubscriptionGrace:
		return true
	case SubscriptionTrial:
		return !s.TrialEndsAt.IsZero() && at.UTC().Before(s.TrialEndsAt.UTC())
	default:
		return false
	}
}

type BillingPeriodState string

const (
	BillingPeriodOpen     BillingPeriodState = "OPEN"
	BillingPeriodClosing  BillingPeriodState = "CLOSING"
	BillingPeriodClosed   BillingPeriodState = "CLOSED"
	BillingPeriodInvoiced BillingPeriodState = "INVOICED"
)

type BillingPeriod struct {
	ID             string
	TenantID       string
	SubscriptionID string
	PlanVersionID  string
	Start          time.Time
	End            time.Time
	State          BillingPeriodState
	Version        int64
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

func NewBillingPeriod(id, tenantID, subscriptionID, planVersionID string, start, end, now time.Time) (BillingPeriod, error) {
	id, tenantID, subscriptionID, planVersionID = strings.TrimSpace(id), strings.TrimSpace(tenantID), strings.TrimSpace(subscriptionID), strings.TrimSpace(planVersionID)
	start, end, now = start.UTC(), end.UTC(), now.UTC()
	if id == "" || tenantID == "" || subscriptionID == "" || planVersionID == "" || !start.Before(end) {
		return BillingPeriod{}, NewError(CodeInvalidArgument, "billing period is invalid")
	}
	return BillingPeriod{ID: id, TenantID: tenantID, SubscriptionID: subscriptionID, PlanVersionID: planVersionID, Start: start, End: end, State: BillingPeriodOpen, Version: 1, CreatedAt: now, UpdatedAt: now}, nil
}
func (p BillingPeriod) Contains(at time.Time) bool {
	at = at.UTC()
	return !at.Before(p.Start) && at.Before(p.End)
}
func (p BillingPeriod) AcceptsUsage(windowStart, windowEnd time.Time) bool {
	return p.State == BillingPeriodOpen && !windowStart.UTC().Before(p.Start) && !windowEnd.UTC().After(p.End) && windowStart.UTC().Before(windowEnd.UTC())
}

type CommercialAccount struct {
	TenantID  string
	State     commercev1.CommercialState
	Version   int64
	CreatedAt time.Time
	UpdatedAt time.Time
}

func NewCommercialAccount(tenantID string, state commercev1.CommercialState, now time.Time) (CommercialAccount, error) {
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return CommercialAccount{}, NewError(CodeInvalidArgument, "tenant is required")
	}
	if state == "" {
		state = commercev1.CommercialActive
	}
	switch state {
	case commercev1.CommercialActive, commercev1.CommercialGrace, commercev1.CommercialSuspended:
	default:
		return CommercialAccount{}, NewError(CodeInvalidArgument, "invalid commercial state")
	}
	now = now.UTC()
	return CommercialAccount{TenantID: tenantID, State: state, Version: 1, CreatedAt: now, UpdatedAt: now}, nil
}
func (a *CommercialAccount) Transition(to commercev1.CommercialState, now time.Time) error {
	if a.State == to {
		return nil
	}
	allowed := map[commercev1.CommercialState]map[commercev1.CommercialState]bool{
		commercev1.CommercialActive:    {commercev1.CommercialGrace: true, commercev1.CommercialSuspended: true, commercev1.CommercialCanceled: true},
		commercev1.CommercialGrace:     {commercev1.CommercialSuspended: true, commercev1.CommercialActive: true, commercev1.CommercialCanceled: true},
		commercev1.CommercialSuspended: {commercev1.CommercialActive: true, commercev1.CommercialCanceled: true},
	}
	if !allowed[a.State][to] {
		return NewError(CodeConflict, "invalid commercial account transition")
	}
	a.State = to
	a.Version++
	a.UpdatedAt = now.UTC()
	return nil
}

type QuotaReservationState string

const (
	QuotaReserved  QuotaReservationState = "RESERVED"
	QuotaCommitted QuotaReservationState = "COMMITTED"
	QuotaReleased  QuotaReservationState = "RELEASED"
	QuotaRejected  QuotaReservationState = "REJECTED"
)

type QuotaReservation struct {
	ID             string
	TenantID       string
	Resource       string
	PolicyVersion  string
	Reason         string
	Quantity       int64
	State          QuotaReservationState
	IdempotencyKey string
	Fingerprint    string
	ExpiresAt      time.Time
	Version        int64
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

func NewQuotaReservation(id, tenantID, resource, policyVersion, key, fingerprint string, quantity int64, expiresAt, now time.Time) (QuotaReservation, error) {
	id, tenantID, resource, policyVersion, key = strings.TrimSpace(id), strings.TrimSpace(tenantID), strings.TrimSpace(resource), strings.TrimSpace(policyVersion), strings.TrimSpace(key)
	if id == "" || tenantID == "" || resource == "" || policyVersion == "" || key == "" || quantity <= 0 || !expiresAt.After(now) {
		return QuotaReservation{}, NewError(CodeInvalidArgument, "quota reservation is invalid")
	}
	now = now.UTC()
	return QuotaReservation{ID: id, TenantID: tenantID, Resource: resource, PolicyVersion: policyVersion, Quantity: quantity, State: QuotaReserved, IdempotencyKey: key, Fingerprint: fingerprint, ExpiresAt: expiresAt.UTC(), Version: 1, CreatedAt: now, UpdatedAt: now}, nil
}
func (q *QuotaReservation) Commit(now time.Time) error {
	if q.State == QuotaCommitted {
		return nil
	}
	if q.State != QuotaReserved {
		return NewError(CodeConflict, "reservation cannot be committed")
	}
	if !now.UTC().Before(q.ExpiresAt) {
		return NewError(CodeConflict, "reservation expired")
	}
	q.State = QuotaCommitted
	q.Version++
	q.UpdatedAt = now.UTC()
	return nil
}
func (q *QuotaReservation) Release(now time.Time) error {
	if q.State == QuotaReleased {
		return nil
	}
	if q.State != QuotaReserved && q.State != QuotaCommitted {
		return NewError(CodeConflict, "reservation cannot be released")
	}
	q.State = QuotaReleased
	q.Version++
	q.UpdatedAt = now.UTC()
	return nil
}
func (q QuotaReservation) CountsAgainstLimit(now time.Time) bool {
	if q.State == QuotaCommitted {
		return true
	}
	return q.State == QuotaReserved && now.UTC().Before(q.ExpiresAt)
}
func (q QuotaReservation) Contract() commercev1.QuotaReservation {
	return commercev1.QuotaReservation{ID: q.ID, TenantID: q.TenantID, Resource: q.Resource, PolicyVersion: q.PolicyVersion, Reason: q.Reason, Quantity: q.Quantity, State: string(q.State), ExpiresAt: q.ExpiresAt, Version: q.Version, CreatedAt: q.CreatedAt, UpdatedAt: q.UpdatedAt}
}
