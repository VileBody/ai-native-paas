package memory

import (
	"context"
	"sort"
	"sync"

	"github.com/keir-research/ai-native-paas/internal/commerce/application"
	"github.com/keir-research/ai-native-paas/internal/commerce/domain"
	commercev1 "github.com/keir-research/ai-native-paas/pkg/contracts/commerce/v1"
)

type data struct {
	definitions   map[string]domain.PlanDefinition
	versions      map[string]domain.PlanVersion
	subscriptions map[string]domain.Subscription
	periods       map[string]domain.BillingPeriod
	accounts      map[string]domain.CommercialAccount
	quotas        map[string]domain.QuotaReservation
	usage         map[string]commercev1.UsageEvent
	idempotency   map[string]application.IdempotencyRecord
	outbox        []application.OutboxRecord
	audit         []application.AuditRecord
	alerts        []application.ReconciliationAlert
}

func newData() data {
	return data{definitions: map[string]domain.PlanDefinition{}, versions: map[string]domain.PlanVersion{}, subscriptions: map[string]domain.Subscription{}, periods: map[string]domain.BillingPeriod{}, accounts: map[string]domain.CommercialAccount{}, quotas: map[string]domain.QuotaReservation{}, usage: map[string]commercev1.UsageEvent{}, idempotency: map[string]application.IdempotencyRecord{}}
}
func (d data) clone() data {
	n := newData()
	for k, v := range d.definitions {
		n.definitions[k] = v
	}
	for k, v := range d.versions {
		v.Spec = domain.ClonePlanSpec(v.Spec)
		n.versions[k] = v
	}
	for k, v := range d.subscriptions {
		n.subscriptions[k] = v
	}
	for k, v := range d.periods {
		n.periods[k] = v
	}
	for k, v := range d.accounts {
		n.accounts[k] = v
	}
	for k, v := range d.quotas {
		n.quotas[k] = v
	}
	for k, v := range d.usage {
		v.Metadata = cloneMetadata(v.Metadata)
		n.usage[k] = v
	}
	for k, v := range d.idempotency {
		n.idempotency[k] = v
	}
	n.outbox = cloneOutbox(d.outbox)
	n.audit = cloneAudit(d.audit)
	n.alerts = append([]application.ReconciliationAlert(nil), d.alerts...)
	return n
}
func cloneOutbox(in []application.OutboxRecord) []application.OutboxRecord {
	out := make([]application.OutboxRecord, len(in))
	for i, v := range in {
		v.Payload = append([]byte(nil), v.Payload...)
		out[i] = v
	}
	return out
}
func cloneAudit(in []application.AuditRecord) []application.AuditRecord {
	out := make([]application.AuditRecord, len(in))
	for i, v := range in {
		v.Data = append([]byte(nil), v.Data...)
		out[i] = v
	}
	return out
}
func cloneMetadata(in map[string]string) map[string]string {
	if in == nil {
		return nil
	}
	out := map[string]string{}
	for k, v := range in {
		out[k] = v
	}
	return out
}

type Store struct {
	mu sync.Mutex
	d  data
}

func New() *Store { return &Store{d: newData()} }
func (s *Store) Transact(ctx context.Context, fn func(application.Tx) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	copy := s.d.clone()
	tx := &transaction{d: &copy}
	if err := fn(tx); err != nil {
		return err
	}
	s.d = copy
	return nil
}

type transaction struct{ d *data }

func (t *transaction) GetPlanDefinition(id string) (domain.PlanDefinition, bool) {
	v, ok := t.d.definitions[id]
	return v, ok
}
func (t *transaction) InsertPlanDefinition(v domain.PlanDefinition) error {
	if _, ok := t.d.definitions[v.ID]; ok {
		return domain.NewError(domain.CodeConflict, "plan definition exists")
	}
	t.d.definitions[v.ID] = v
	return nil
}
func (t *transaction) UpdatePlanDefinition(v domain.PlanDefinition, expected int64) error {
	old, ok := t.d.definitions[v.ID]
	if !ok {
		return domain.NewError(domain.CodeNotFound, "plan definition not found")
	}
	if old.Version != expected {
		return domain.NewError(domain.CodeStaleVersion, "plan definition stale")
	}
	t.d.definitions[v.ID] = v
	return nil
}
func (t *transaction) GetPlanVersion(id string) (domain.PlanVersion, bool) {
	v, ok := t.d.versions[id]
	v.Spec = domain.ClonePlanSpec(v.Spec)
	return v, ok
}
func (t *transaction) ListPlanVersions(definition string) []domain.PlanVersion {
	out := []domain.PlanVersion{}
	for _, v := range t.d.versions {
		if v.DefinitionID == definition {
			v.Spec = domain.ClonePlanSpec(v.Spec)
			out = append(out, v)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Number < out[j].Number })
	return out
}
func (t *transaction) InsertPlanVersion(v domain.PlanVersion) error {
	if _, ok := t.d.versions[v.ID]; ok {
		return domain.NewError(domain.CodeConflict, "plan version exists")
	}
	v.Spec = domain.ClonePlanSpec(v.Spec)
	t.d.versions[v.ID] = v
	return nil
}
func (t *transaction) UpdatePlanVersion(v domain.PlanVersion, expected int64) error {
	old, ok := t.d.versions[v.ID]
	if !ok {
		return domain.NewError(domain.CodeNotFound, "plan version not found")
	}
	if old.Version != expected {
		return domain.NewError(domain.CodeStaleVersion, "plan version stale")
	}
	if old.State != domain.PlanDraft && (old.Spec.Currency != v.Spec.Currency || old.PolicyVersion != v.PolicyVersion || old.DefinitionID != v.DefinitionID || old.Number != v.Number) {
		return domain.NewError(domain.CodeConflict, "active plan immutable")
	}
	v.Spec = domain.ClonePlanSpec(v.Spec)
	t.d.versions[v.ID] = v
	return nil
}
func (t *transaction) GetSubscription(id string) (domain.Subscription, bool) {
	v, ok := t.d.subscriptions[id]
	return v, ok
}
func (t *transaction) FindSubscriptionByTenant(tenant string) (domain.Subscription, bool) {
	var found domain.Subscription
	ok := false
	for _, v := range t.d.subscriptions {
		if v.TenantID == tenant && (!ok || v.CreatedAt.After(found.CreatedAt)) {
			found = v
			ok = true
		}
	}
	return found, ok
}
func (t *transaction) InsertSubscription(v domain.Subscription) error {
	if _, ok := t.d.subscriptions[v.ID]; ok {
		return domain.NewError(domain.CodeConflict, "subscription exists")
	}
	t.d.subscriptions[v.ID] = v
	return nil
}
func (t *transaction) UpdateSubscription(v domain.Subscription, expected int64) error {
	old, ok := t.d.subscriptions[v.ID]
	if !ok {
		return domain.NewError(domain.CodeNotFound, "subscription not found")
	}
	if old.Version != expected {
		return domain.NewError(domain.CodeStaleVersion, "subscription stale")
	}
	t.d.subscriptions[v.ID] = v
	return nil
}
func (t *transaction) GetBillingPeriod(id string) (domain.BillingPeriod, bool) {
	v, ok := t.d.periods[id]
	return v, ok
}
func (t *transaction) ListBillingPeriods(tenant string) []domain.BillingPeriod {
	out := []domain.BillingPeriod{}
	for _, v := range t.d.periods {
		if v.TenantID == tenant {
			out = append(out, v)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Start.Before(out[j].Start) })
	return out
}
func (t *transaction) InsertBillingPeriod(v domain.BillingPeriod) error {
	if _, ok := t.d.periods[v.ID]; ok {
		return domain.NewError(domain.CodeConflict, "billing period exists")
	}
	for _, p := range t.d.periods {
		if p.SubscriptionID == v.SubscriptionID && v.Start.Before(p.End) && p.Start.Before(v.End) {
			return domain.NewError(domain.CodeConflict, "billing periods overlap")
		}
	}
	t.d.periods[v.ID] = v
	return nil
}
func (t *transaction) UpdateBillingPeriod(v domain.BillingPeriod, expected int64) error {
	old, ok := t.d.periods[v.ID]
	if !ok {
		return domain.NewError(domain.CodeNotFound, "billing period not found")
	}
	if old.Version != expected {
		return domain.NewError(domain.CodeStaleVersion, "billing period stale")
	}
	t.d.periods[v.ID] = v
	return nil
}
func (t *transaction) GetCommercialAccount(tenant string) (domain.CommercialAccount, bool) {
	v, ok := t.d.accounts[tenant]
	return v, ok
}
func (t *transaction) InsertCommercialAccount(v domain.CommercialAccount) error {
	if _, ok := t.d.accounts[v.TenantID]; ok {
		return domain.NewError(domain.CodeConflict, "account exists")
	}
	t.d.accounts[v.TenantID] = v
	return nil
}
func (t *transaction) UpdateCommercialAccount(v domain.CommercialAccount, expected int64) error {
	old, ok := t.d.accounts[v.TenantID]
	if !ok {
		return domain.NewError(domain.CodeNotFound, "account not found")
	}
	if old.Version != expected {
		return domain.NewError(domain.CodeStaleVersion, "account stale")
	}
	t.d.accounts[v.TenantID] = v
	return nil
}
func (t *transaction) GetQuotaReservation(id string) (domain.QuotaReservation, bool) {
	v, ok := t.d.quotas[id]
	return v, ok
}
func (t *transaction) FindQuotaReservation(tenant, key string) (domain.QuotaReservation, bool) {
	for _, v := range t.d.quotas {
		if v.TenantID == tenant && v.IdempotencyKey == key {
			return v, true
		}
	}
	return domain.QuotaReservation{}, false
}
func (t *transaction) ListQuotaReservations(tenant, resource string) []domain.QuotaReservation {
	out := []domain.QuotaReservation{}
	for _, v := range t.d.quotas {
		if v.TenantID == tenant && (resource == "" || v.Resource == resource) {
			out = append(out, v)
		}
	}
	return out
}
func (t *transaction) InsertQuotaReservation(v domain.QuotaReservation) error {
	if _, ok := t.d.quotas[v.ID]; ok {
		return domain.NewError(domain.CodeConflict, "quota reservation exists")
	}
	if _, ok := t.FindQuotaReservation(v.TenantID, v.IdempotencyKey); ok {
		return domain.NewError(domain.CodeConflict, "quota idempotency exists")
	}
	t.d.quotas[v.ID] = v
	return nil
}
func (t *transaction) UpdateQuotaReservation(v domain.QuotaReservation, expected int64) error {
	old, ok := t.d.quotas[v.ID]
	if !ok {
		return domain.NewError(domain.CodeNotFound, "quota reservation not found")
	}
	if old.Version != expected {
		return domain.NewError(domain.CodeStaleVersion, "quota reservation stale")
	}
	t.d.quotas[v.ID] = v
	return nil
}
func usageKey(tenant, key string) string { return tenant + "\x00" + key }
func (t *transaction) FindUsageByKey(tenant, key string) (commercev1.UsageEvent, bool) {
	v, ok := t.d.usage[usageKey(tenant, key)]
	v.Metadata = cloneMetadata(v.Metadata)
	return v, ok
}
func (t *transaction) ListUsage(tenant, period string) []commercev1.UsageEvent {
	out := []commercev1.UsageEvent{}
	for _, v := range t.d.usage {
		if v.TenantID == tenant && (period == "" || v.PeriodID == period) {
			v.Metadata = cloneMetadata(v.Metadata)
			out = append(out, v)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].OccurredAt.Equal(out[j].OccurredAt) {
			return out[i].OccurredAt.Before(out[j].OccurredAt)
		}
		return out[i].ID < out[j].ID
	})
	return out
}
func (t *transaction) InsertUsage(v commercev1.UsageEvent) error {
	k := usageKey(v.TenantID, v.IdempotencyKey)
	if _, ok := t.d.usage[k]; ok {
		return domain.NewError(domain.CodeConflict, "usage key exists")
	}
	v.Metadata = cloneMetadata(v.Metadata)
	t.d.usage[k] = v
	return nil
}
func idemKey(tenant, scope, key string) string { return tenant + "\x00" + scope + "\x00" + key }
func (t *transaction) GetIdempotency(tenant, scope, key string) (application.IdempotencyRecord, bool) {
	v, ok := t.d.idempotency[idemKey(tenant, scope, key)]
	return v, ok
}
func (t *transaction) InsertIdempotency(v application.IdempotencyRecord) error {
	k := idemKey(v.TenantID, v.Scope, v.Key)
	if _, ok := t.d.idempotency[k]; ok {
		return domain.NewError(domain.CodeConflict, "idempotency exists")
	}
	t.d.idempotency[k] = v
	return nil
}
func (t *transaction) AppendOutbox(v application.OutboxRecord) error {
	v.Payload = append([]byte(nil), v.Payload...)
	t.d.outbox = append(t.d.outbox, v)
	return nil
}
func (t *transaction) AppendAudit(v application.AuditRecord) error {
	v.Data = append([]byte(nil), v.Data...)
	t.d.audit = append(t.d.audit, v)
	return nil
}
func (t *transaction) AppendAlert(v application.ReconciliationAlert) error {
	t.d.alerts = append(t.d.alerts, v)
	return nil
}
func (t *transaction) ListAlerts(tenant, period string) []application.ReconciliationAlert {
	out := []application.ReconciliationAlert{}
	for _, v := range t.d.alerts {
		if v.TenantID == tenant && (period == "" || v.PeriodID == period) {
			out = append(out, v)
		}
	}
	return out
}

func (s *Store) SnapshotCounts() (usage, quotas, outbox, audit, alerts int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.d.usage), len(s.d.quotas), len(s.d.outbox), len(s.d.audit), len(s.d.alerts)
}
func (s *Store) Usage(tenant, period string) []commercev1.UsageEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx := transaction{d: &s.d}
	return tx.ListUsage(tenant, period)
}
func (s *Store) Outbox() []application.OutboxRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneOutbox(s.d.outbox)
}
func (s *Store) Alerts(tenant, period string) []application.ReconciliationAlert {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx := transaction{d: &s.d}
	return tx.ListAlerts(tenant, period)
}

var _ application.Store = (*Store)(nil)
var _ application.Tx = (*transaction)(nil)
