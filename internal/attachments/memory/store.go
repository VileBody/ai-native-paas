// Package memory provides a serializable in-memory Attachments store.
package memory

import (
	"context"
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/keir-research/ai-native-paas/internal/attachments/application"
	"github.com/keir-research/ai-native-paas/internal/attachments/domain"
	attachmentsv1 "github.com/keir-research/ai-native-paas/pkg/contracts/attachments/v1"
)

type state struct {
	SecretSets  map[string]domain.SecretSet
	Secrets     map[string]domain.SecretMetadata
	Plans       map[string]domain.ServicePlan
	Instances   map[string]domain.ServiceInstance
	Bindings    map[string]domain.ServiceBinding
	Claims      map[string]domain.DomainClaim
	Snapshots   map[string]domain.AttachmentSnapshot
	Idempotency map[string]domain.IdempotencyRecord
	Outbox      []domain.OutboxRecord
	Audit       []domain.AuditRecord
}

func newState() *state {
	return &state{
		SecretSets: map[string]domain.SecretSet{}, Secrets: map[string]domain.SecretMetadata{},
		Plans: map[string]domain.ServicePlan{}, Instances: map[string]domain.ServiceInstance{},
		Bindings: map[string]domain.ServiceBinding{}, Claims: map[string]domain.DomainClaim{},
		Snapshots: map[string]domain.AttachmentSnapshot{}, Idempotency: map[string]domain.IdempotencyRecord{},
	}
}

func clone[T any](value T) T {
	raw, _ := json.Marshal(value)
	var out T
	_ = json.Unmarshal(raw, &out)
	return out
}

func (s *state) clone() *state { return clone(s) }

type Store struct {
	mu    sync.Mutex
	state *state
}

var _ application.Store = (*Store)(nil)

func New() *Store { return &Store{state: newState()} }

func (s *Store) Transact(ctx context.Context, fn func(application.Tx) error) error {
	if s == nil || fn == nil {
		return domain.NewError(domain.CodeInvalidArgument, "transaction callback required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	working := s.state.clone()
	if err := fn((*tx)(working)); err != nil {
		return err
	}
	s.state = working
	return nil
}

func (s *Store) Audit() []domain.AuditRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	return clone(s.state.Audit)
}

func (s *Store) Outbox() []domain.OutboxRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	return clone(s.state.Outbox)
}

type tx state

var _ application.Tx = (*tx)(nil)

func stale(name string) error {
	return domain.NewError(domain.CodeStaleVersion, name+" version is stale")
}
func conflict(name string) error               { return domain.NewError(domain.CodeConflict, name+" already exists") }
func planKey(id string, version int64) string  { return id + "\x00" + strconv.FormatInt(version, 10) }
func idemKey(tenant, scope, key string) string { return tenant + "\x00" + scope + "\x00" + key }

func (t *tx) GetSecretSet(id string) (domain.SecretSet, bool) {
	v, ok := t.SecretSets[id]
	return clone(v), ok
}
func (t *tx) FindSecretSet(tenant, environment string) (domain.SecretSet, bool) {
	for _, v := range t.SecretSets {
		if v.TenantID == tenant && v.EnvironmentID == environment {
			return clone(v), true
		}
	}
	return domain.SecretSet{}, false
}
func (t *tx) PutSecretSet(v domain.SecretSet, expected int64) error {
	old, ok := t.SecretSets[v.ID]
	if expected == 0 {
		if ok {
			return conflict("secret set")
		}
		if _, exists := t.FindSecretSet(v.TenantID, v.EnvironmentID); exists {
			return conflict("secret set")
		}
	} else if !ok || old.Version != expected {
		return stale("secret set")
	}
	t.SecretSets[v.ID] = clone(v)
	return nil
}

func (t *tx) GetSecret(id string) (domain.SecretMetadata, bool) {
	v, ok := t.Secrets[id]
	return clone(v), ok
}
func (t *tx) FindSecret(set, name string, scope attachmentsv1.SecretScope) (domain.SecretMetadata, bool) {
	for _, v := range t.Secrets {
		if v.SecretSetID == set && v.Name == name && v.Scope == scope {
			return clone(v), true
		}
	}
	return domain.SecretMetadata{}, false
}
func (t *tx) ListSecrets(set string) []domain.SecretMetadata {
	out := []domain.SecretMetadata{}
	for _, v := range t.Secrets {
		if v.SecretSetID == set {
			out = append(out, clone(v))
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name == out[j].Name {
			return out[i].ID < out[j].ID
		}
		return out[i].Name < out[j].Name
	})
	return out
}
func (t *tx) PutSecret(v domain.SecretMetadata, expected int64) error {
	old, ok := t.Secrets[v.ID]
	if expected == 0 {
		if ok {
			return conflict("secret")
		}
		if _, exists := t.FindSecret(v.SecretSetID, v.Name, v.Scope); exists {
			return conflict("secret")
		}
	} else if !ok || old.Version != expected {
		return stale("secret")
	}
	t.Secrets[v.ID] = clone(v)
	return nil
}

func (t *tx) GetPlan(id string, version int64) (domain.ServicePlan, bool) {
	v, ok := t.Plans[planKey(id, version)]
	return clone(v), ok
}
func (t *tx) LatestPlan(id string) (domain.ServicePlan, bool) {
	var out domain.ServicePlan
	found := false
	for _, v := range t.Plans {
		if v.ID == id && (!found || v.Version > out.Version) {
			out, found = v, true
		}
	}
	return clone(out), found
}
func (t *tx) ListPlans() []domain.ServicePlan {
	out := make([]domain.ServicePlan, 0, len(t.Plans))
	for _, v := range t.Plans {
		out = append(out, clone(v))
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ID == out[j].ID {
			return out[i].Version < out[j].Version
		}
		return out[i].ID < out[j].ID
	})
	return out
}
func (t *tx) PutPlan(v domain.ServicePlan) error {
	key := planKey(v.ID, v.Version)
	if old, ok := t.Plans[key]; ok {
		if domain.Hash(old) == domain.Hash(v) {
			return nil
		}
		return conflict("service plan version")
	}
	t.Plans[key] = clone(v)
	return nil
}

func (t *tx) GetInstance(id string) (domain.ServiceInstance, bool) {
	v, ok := t.Instances[id]
	return clone(v), ok
}
func (t *tx) FindInstance(tenant, environment, name string) (domain.ServiceInstance, bool) {
	for _, v := range t.Instances {
		if v.TenantID == tenant && v.EnvironmentID == environment && strings.EqualFold(v.Name, name) && v.State != attachmentsv1.ServiceDeleted {
			return clone(v), true
		}
	}
	return domain.ServiceInstance{}, false
}
func (t *tx) ListInstances(tenant, applicationID string) []domain.ServiceInstance {
	out := []domain.ServiceInstance{}
	for _, v := range t.Instances {
		if v.TenantID == tenant && v.ApplicationID == applicationID {
			out = append(out, clone(v))
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].ID < out[j].ID
		}
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
	return out
}
func (t *tx) PutInstance(v domain.ServiceInstance, expected int64) error {
	old, ok := t.Instances[v.ID]
	if expected == 0 {
		if ok {
			return conflict("service instance")
		}
		if _, exists := t.FindInstance(v.TenantID, v.EnvironmentID, v.Name); exists {
			return conflict("service instance")
		}
	} else if !ok || old.Version != expected {
		return stale("service instance")
	}
	if ok && old.ProviderID != "" && v.ProviderID != old.ProviderID {
		return conflict("provider identity")
	}
	t.Instances[v.ID] = clone(v)
	return nil
}

func (t *tx) GetBinding(id string) (domain.ServiceBinding, bool) {
	v, ok := t.Bindings[id]
	return clone(v), ok
}
func (t *tx) FindBinding(tenant, environment, instance string) (domain.ServiceBinding, bool) {
	for _, v := range t.Bindings {
		if v.TenantID == tenant && v.EnvironmentID == environment && v.InstanceID == instance && v.State != attachmentsv1.BindingRevoked {
			return clone(v), true
		}
	}
	return domain.ServiceBinding{}, false
}
func (t *tx) listBindings(match func(domain.ServiceBinding) bool) []domain.ServiceBinding {
	out := []domain.ServiceBinding{}
	for _, v := range t.Bindings {
		if match(v) {
			out = append(out, clone(v))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
func (t *tx) ListBindings(tenant, environment string) []domain.ServiceBinding {
	return t.listBindings(func(v domain.ServiceBinding) bool { return v.TenantID == tenant && v.EnvironmentID == environment })
}
func (t *tx) ListAppBindings(tenant, applicationID string) []domain.ServiceBinding {
	return t.listBindings(func(v domain.ServiceBinding) bool { return v.TenantID == tenant && v.ApplicationID == applicationID })
}
func (t *tx) PutBinding(v domain.ServiceBinding, expected int64) error {
	old, ok := t.Bindings[v.ID]
	if expected == 0 {
		if ok {
			return conflict("binding")
		}
		if _, exists := t.FindBinding(v.TenantID, v.EnvironmentID, v.InstanceID); exists {
			return conflict("binding")
		}
	} else if !ok || old.Version != expected {
		return stale("binding")
	}
	t.Bindings[v.ID] = clone(v)
	return nil
}

func (t *tx) GetClaim(id string) (domain.DomainClaim, bool) {
	v, ok := t.Claims[id]
	return clone(v), ok
}
func (t *tx) FindClaim(host string) (domain.DomainClaim, bool) {
	for _, v := range t.Claims {
		if strings.EqualFold(v.Hostname, host) && v.State != attachmentsv1.DomainReleased {
			return clone(v), true
		}
	}
	return domain.DomainClaim{}, false
}
func (t *tx) ListClaims(tenant, environment string) []domain.DomainClaim {
	out := []domain.DomainClaim{}
	for _, v := range t.Claims {
		if v.TenantID == tenant && v.EnvironmentID == environment {
			out = append(out, clone(v))
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Hostname == out[j].Hostname {
			return out[i].ID < out[j].ID
		}
		return out[i].Hostname < out[j].Hostname
	})
	return out
}
func (t *tx) PutClaim(v domain.DomainClaim, expected int64) error {
	old, ok := t.Claims[v.ID]
	if expected == 0 {
		if ok {
			return conflict("domain claim")
		}
		if _, exists := t.FindClaim(v.Hostname); exists {
			return conflict("domain claim")
		}
	} else if !ok || old.Version != expected {
		return stale("domain claim")
	}
	t.Claims[v.ID] = clone(v)
	return nil
}

func (t *tx) GetSnapshot(id string) (domain.AttachmentSnapshot, bool) {
	v, ok := t.Snapshots[id]
	return clone(v), ok
}
func (t *tx) LatestSnapshot(tenant, environment string) (domain.AttachmentSnapshot, bool) {
	var out domain.AttachmentSnapshot
	found := false
	for _, v := range t.Snapshots {
		if v.Value.TenantID == tenant && v.Value.EnvironmentID == environment && (!found || v.Value.Version > out.Value.Version) {
			out, found = v, true
		}
	}
	return clone(out), found
}
func (t *tx) FindSnapshot(tenant, environment, hash string) (domain.AttachmentSnapshot, bool) {
	for _, v := range t.Snapshots {
		if v.Value.TenantID == tenant && v.Value.EnvironmentID == environment && v.ContentHash == hash {
			return clone(v), true
		}
	}
	return domain.AttachmentSnapshot{}, false
}
func (t *tx) PutSnapshot(v domain.AttachmentSnapshot) error {
	if _, ok := t.Snapshots[v.Value.SnapshotID]; ok {
		return conflict("snapshot")
	}
	for _, old := range t.Snapshots {
		if old.Value.TenantID == v.Value.TenantID && old.Value.EnvironmentID == v.Value.EnvironmentID && (old.Value.Version == v.Value.Version || old.ContentHash == v.ContentHash) {
			return conflict("snapshot")
		}
	}
	t.Snapshots[v.Value.SnapshotID] = clone(v)
	return nil
}

func (t *tx) GetIdempotency(tenant, scope, key string) (domain.IdempotencyRecord, bool) {
	v, ok := t.Idempotency[idemKey(tenant, scope, key)]
	return clone(v), ok
}
func (t *tx) PutIdempotency(v domain.IdempotencyRecord) error {
	key := idemKey(v.TenantID, v.Scope, v.Key)
	if old, ok := t.Idempotency[key]; ok {
		if old.RequestHash == v.RequestHash && old.ResourceID == v.ResourceID {
			return nil
		}
		return conflict("idempotency key")
	}
	t.Idempotency[key] = clone(v)
	return nil
}
func (t *tx) AppendOutbox(v domain.OutboxRecord) error {
	for _, old := range t.Outbox {
		if old.ID == v.ID {
			return conflict("outbox record")
		}
	}
	t.Outbox = append(t.Outbox, clone(v))
	return nil
}
func (t *tx) AppendAudit(v domain.AuditRecord) error {
	for _, old := range t.Audit {
		if old.ID == v.ID {
			return conflict("audit record")
		}
	}
	t.Audit = append(t.Audit, clone(v))
	return nil
}
