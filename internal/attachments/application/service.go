package application

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/keir-research/ai-native-paas/internal/attachments/domain"
	attachmentsv1 "github.com/keir-research/ai-native-paas/pkg/contracts/attachments/v1"
	commercev1 "github.com/keir-research/ai-native-paas/pkg/contracts/commerce/v1"
	"sort"
	"strings"
	"time"
)

type Service struct {
	Store                                 Store
	Environments                          EnvironmentDirectory
	Secrets                               SecretProvider
	Provider                              ManagedServiceProvider
	DNS                                   DNSResolver
	Certificates                          CertificateProvider
	Approvals                             ApprovalVerifier
	Runtime                               RuntimeSnapshotPublisher
	Commerce                              CommercialEntitlementPort
	Usage                                 UsageSink
	Logger                                StructuredLogger
	Clock                                 Clock
	IDs                                   IDGenerator
	DefaultDomain                         string
	DNSObservationDelay, DomainQuarantine time.Duration
}

func (s *Service) defaults() {
	if s.Clock == nil {
		s.Clock = RealClock{}
	}
	if s.IDs == nil {
		s.IDs = &SequentialIDs{}
	}
	if s.DefaultDomain == "" {
		s.DefaultDomain = "apps.example.test"
	}
	if s.DNSObservationDelay <= 0 {
		s.DNSObservationDelay = time.Minute
	}
	if s.DomainQuarantine <= 0 {
		s.DomainQuarantine = 24 * time.Hour
	}
}
func (s *Service) env(ctx context.Context, tenant, id string) (EnvironmentRef, error) {
	if s.Environments == nil {
		return EnvironmentRef{}, domain.NewError(domain.CodeUnavailable, "environment directory unavailable")
	}
	v, e := s.Environments.ResolveEnvironment(ctx, tenant, id)
	if e != nil {
		return EnvironmentRef{}, e
	}
	if v.TenantID != tenant {
		return EnvironmentRef{}, domain.NewError(domain.CodeForbidden, "environment belongs to another tenant")
	}
	if !v.Ready {
		return EnvironmentRef{}, domain.NewError(domain.CodeConflict, "environment not ready")
	}
	return v, nil
}
func reqHash(v any) string { return domain.Hash(v) }
func idem(tx Tx, tenant, scope, key, hash string) (string, bool, error) {
	if strings.TrimSpace(key) == "" {
		return "", false, domain.NewError(domain.CodeInvalidArgument, "idempotency key required")
	}
	if r, ok := tx.GetIdempotency(tenant, scope, key); ok {
		if r.RequestHash != hash {
			return "", false, domain.NewError(domain.CodeConflict, "idempotency key reused")
		}
		return r.ResourceID, true, nil
	}
	return "", false, nil
}
func (s *Service) records(tx Tx, now time.Time, tenant, actor, topic, id string, payload any) error {
	b, _ := json.Marshal(payload)
	if err := tx.AppendOutbox(domain.OutboxRecord{ID: s.IDs.New("evt"), TenantID: tenant, Topic: topic, AggregateID: id, Payload: b, CreatedAt: now}); err != nil {
		return err
	}
	return tx.AppendAudit(domain.AuditRecord{ID: s.IDs.New("aud"), TenantID: tenant, ActorID: actor, Action: topic, ResourceType: "attachment", ResourceID: id, Data: b, CreatedAt: now})
}
func (s *Service) secretSet(tx Tx, e EnvironmentRef, now time.Time) (domain.SecretSet, error) {
	if v, ok := tx.FindSecretSet(e.TenantID, e.EnvironmentID); ok {
		return v, nil
	}
	v := domain.SecretSet{ID: s.IDs.New("secset"), TenantID: e.TenantID, ApplicationID: e.ApplicationID, EnvironmentID: e.EnvironmentID, ProviderPath: fmt.Sprintf("tenants/%s/apps/%s/%s", e.TenantID, e.ApplicationID, e.EnvironmentID), Version: 1, CreatedAt: now, UpdatedAt: now}
	if err := tx.PutSecretSet(v, 0); err != nil {
		return v, err
	}
	return v, nil
}
func (s *Service) snapshot(tx Tx, e EnvironmentRef, now time.Time) (domain.AttachmentSnapshot, bool, error) {
	set, err := s.secretSet(tx, e, now)
	if err != nil {
		return domain.AttachmentSnapshot{}, false, err
	}
	bindings := []string{}
	for _, b := range tx.ListBindings(e.TenantID, e.EnvironmentID) {
		if (b.State == attachmentsv1.BindingActive || b.State == attachmentsv1.BindingRotating) && b.CredentialRef != "" {
			h := domain.Hash(b.CredentialRef)
			bindings = append(bindings, b.ID+":"+h[:12])
		}
	}
	domains := []string{}
	for _, c := range tx.ListClaims(e.TenantID, e.EnvironmentID) {
		if c.State == attachmentsv1.DomainActive {
			domains = append(domains, c.Hostname)
		}
	}
	sort.Strings(bindings)
	sort.Strings(domains)
	setRef := fmt.Sprintf("%s:v%d", set.ID, set.Version)
	content := domain.Hash(struct {
		S    string
		B, D []string
	}{setRef, bindings, domains})
	if old, ok := tx.FindSnapshot(e.TenantID, e.EnvironmentID, content); ok {
		return old, false, nil
	}
	version := int64(1)
	if old, ok := tx.LatestSnapshot(e.TenantID, e.EnvironmentID); ok {
		version = old.Value.Version + 1
	}
	value := attachmentsv1.AttachmentSnapshot{SnapshotID: s.IDs.New("ats"), TenantID: e.TenantID, ApplicationID: e.ApplicationID, EnvironmentID: e.EnvironmentID, Version: version, SecretSetRef: setRef, ServiceBindings: bindings, ActiveDomains: domains, CreatedAt: now}
	if err := value.Validate(); err != nil {
		return domain.AttachmentSnapshot{}, false, domain.NewError(domain.CodeInternal, err.Error())
	}
	snap := domain.AttachmentSnapshot{Value: value, ContentHash: content}
	if err := tx.PutSnapshot(snap); err != nil {
		return snap, false, err
	}
	return snap, true, nil
}
func (s *Service) publish(ctx context.Context, v domain.AttachmentSnapshot) error {
	if s.Runtime == nil {
		return nil
	}
	if err := s.Runtime.Publish(ctx, v.Value); err != nil {
		return domain.NewError(domain.CodeRetryable, "runtime snapshot publish failed")
	}
	return nil
}
func (s *Service) Resolve(ctx context.Context, tenant, env string) (attachmentsv1.AttachmentSnapshot, error) {
	s.defaults()
	if _, err := s.env(ctx, tenant, env); err != nil {
		return attachmentsv1.AttachmentSnapshot{}, err
	}
	var out domain.AttachmentSnapshot
	err := s.Store.Transact(ctx, func(tx Tx) error {
		var ok bool
		out, ok = tx.LatestSnapshot(tenant, env)
		if !ok {
			return domain.NewError(domain.CodeNotFound, "snapshot not found")
		}
		return nil
	})
	return out.Value, err
}

func (s *Service) SetSecret(ctx context.Context, r SetSecretRequest) (attachmentsv1.SecretMetadata, attachmentsv1.AttachmentSnapshotRef, error) {
	s.defaults()
	e, err := s.env(ctx, r.TenantID, r.EnvironmentID)
	if err != nil {
		return attachmentsv1.SecretMetadata{}, attachmentsv1.AttachmentSnapshotRef{}, err
	}
	if e.ApplicationID != r.ApplicationID {
		return attachmentsv1.SecretMetadata{}, attachmentsv1.AttachmentSnapshotRef{}, domain.NewError(domain.CodeForbidden, "application mismatch")
	}
	if !domain.ValidSecretName(r.Name) || len(r.Value) == 0 {
		return attachmentsv1.SecretMetadata{}, attachmentsv1.AttachmentSnapshotRef{}, domain.NewError(domain.CodeInvalidArgument, "invalid secret")
	}
	candidate := attachmentsv1.SecretMetadata{Name: r.Name, Scope: r.Scope, Phase: r.Phase, Version: 1, ExpiresAt: r.ExpiresAt, UpdatedAt: s.Clock.Now()}
	if err = candidate.Validate(); err != nil {
		return attachmentsv1.SecretMetadata{}, attachmentsv1.AttachmentSnapshotRef{}, domain.NewError(domain.CodeInvalidArgument, err.Error())
	}
	hash := reqHash(struct {
		T, A, E, N string
		S          attachmentsv1.SecretScope
		P          attachmentsv1.SecretPhase
		V, X       string
	}{r.TenantID, r.ApplicationID, r.EnvironmentID, r.Name, r.Scope, r.Phase, domain.Hash(r.Value), r.ExpiresAt.UTC().String()})
	var previousID string
	if err = s.Store.Transact(ctx, func(tx Tx) error {
		if id, ok, e := idem(tx, r.TenantID, "secret.set", r.IdempotencyKey, hash); e != nil {
			return e
		} else if ok {
			previousID = id
		}
		return nil
	}); err != nil {
		return attachmentsv1.SecretMetadata{}, attachmentsv1.AttachmentSnapshotRef{}, err
	}
	if previousID != "" {
		var m domain.SecretMetadata
		var snap domain.AttachmentSnapshot
		err = s.Store.Transact(ctx, func(tx Tx) error {
			m, _ = tx.GetSecret(previousID)
			snap, _ = tx.LatestSnapshot(r.TenantID, r.EnvironmentID)
			return nil
		})
		return m.Public(), attachmentsv1.AttachmentSnapshotRef{SnapshotID: snap.Value.SnapshotID, EnvironmentID: snap.Value.EnvironmentID, Version: snap.Value.Version}, err
	}
	if s.Secrets == nil {
		return attachmentsv1.SecretMetadata{}, attachmentsv1.AttachmentSnapshotRef{}, domain.NewError(domain.CodeUnavailable, "secret provider unavailable")
	}
	path := fmt.Sprintf("tenants/%s/apps/%s/%s", r.TenantID, r.ApplicationID, r.EnvironmentID)
	write, err := s.Secrets.Write(ctx, r.TenantID, path, r.Name, append([]byte(nil), r.Value...), r.ExpiresAt)
	if err != nil {
		return attachmentsv1.SecretMetadata{}, attachmentsv1.AttachmentSnapshotRef{}, domain.NewError(domain.CodeRetryable, "secret provider write failed")
	}
	now := s.Clock.Now()
	var meta domain.SecretMetadata
	var snap domain.AttachmentSnapshot
	err = s.Store.Transact(ctx, func(tx Tx) error {
		set, e2 := s.secretSet(tx, e, now)
		if e2 != nil {
			return e2
		}
		if old, ok := tx.FindSecret(set.ID, r.Name, r.Scope); ok && !old.Deleted {
			expected := old.Version
			old.ProviderRef = write.Ref
			old.ProviderVersion = write.Version
			old.ExpiresAt = r.ExpiresAt
			old.Version++
			old.UpdatedAt = now
			meta = old
			if err := tx.PutSecret(old, expected); err != nil {
				return err
			}
		} else {
			meta = domain.SecretMetadata{ID: s.IDs.New("sec"), TenantID: r.TenantID, SecretSetID: set.ID, ApplicationID: r.ApplicationID, EnvironmentID: r.EnvironmentID, Name: r.Name, Scope: r.Scope, Phase: r.Phase, ProviderRef: write.Ref, ProviderVersion: write.Version, Version: 1, ExpiresAt: r.ExpiresAt, CreatedAt: now, UpdatedAt: now}
			if err := tx.PutSecret(meta, 0); err != nil {
				return err
			}
		}
		if r.Scope == attachmentsv1.SecretScopeRuntime {
			expected := set.Version
			set.Version++
			set.UpdatedAt = now
			if err := tx.PutSecretSet(set, expected); err != nil {
				return err
			}
		}
		snap, _, e2 = s.snapshot(tx, e, now)
		if e2 != nil {
			return e2
		}
		if err := tx.PutIdempotency(domain.IdempotencyRecord{TenantID: r.TenantID, Scope: "secret.set", Key: r.IdempotencyKey, RequestHash: hash, ResourceID: meta.ID, CreatedAt: now}); err != nil {
			return err
		}
		return s.records(tx, now, r.TenantID, r.ActorID, "attachments.secret.set", meta.ID, map[string]any{"secret_id": meta.ID, "name": meta.Name, "scope": meta.Scope, "version": meta.Version, "snapshot_id": snap.Value.SnapshotID})
	})
	if err != nil {
		return attachmentsv1.SecretMetadata{}, attachmentsv1.AttachmentSnapshotRef{}, err
	}
	if r.Scope == attachmentsv1.SecretScopeRuntime {
		if err = s.publish(ctx, snap); err != nil {
			return meta.Public(), attachmentsv1.AttachmentSnapshotRef{}, err
		}
	}
	if s.Logger != nil {
		s.Logger.Log(ctx, "attachments.secret.set", map[string]string{"tenant_id": r.TenantID, "secret_id": meta.ID, "value": "[REDACTED]"})
	}
	return meta.Public(), attachmentsv1.AttachmentSnapshotRef{SnapshotID: snap.Value.SnapshotID, EnvironmentID: snap.Value.EnvironmentID, Version: snap.Value.Version}, nil
}
func (s *Service) GetSecretMetadata(ctx context.Context, tenant, id string) (attachmentsv1.SecretMetadata, error) {
	var out domain.SecretMetadata
	err := s.Store.Transact(ctx, func(tx Tx) error {
		var ok bool
		out, ok = tx.GetSecret(id)
		if !ok {
			return domain.NewError(domain.CodeNotFound, "secret not found")
		}
		if out.TenantID != tenant {
			return domain.NewError(domain.CodeForbidden, "cross-tenant secret")
		}
		return nil
	})
	return out.Public(), err
}
func (s *Service) ListSecretMetadata(ctx context.Context, tenant, env string) ([]attachmentsv1.SecretMetadata, error) {
	if _, err := s.env(ctx, tenant, env); err != nil {
		return nil, err
	}
	return s.listSecretMetadata(ctx, tenant, env)
}

func (s *Service) listSecretMetadata(ctx context.Context, tenant, env string) ([]attachmentsv1.SecretMetadata, error) {
	out := []attachmentsv1.SecretMetadata{}
	err := s.Store.Transact(ctx, func(tx Tx) error {
		set, ok := tx.FindSecretSet(tenant, env)
		if !ok {
			return nil
		}
		for _, m := range tx.ListSecrets(set.ID) {
			out = append(out, m.Public())
		}
		return nil
	})
	return out, err
}

// ListProjectSecretMetadata is the Project MCP boundary. Unlike the v1
// compatibility port, it binds the environment to the project/application
// identity taken from the verified agent credential.
func (s *Service) ListProjectSecretMetadata(ctx context.Context, tenant, applicationID, environmentID string) ([]attachmentsv1.SecretMetadata, error) {
	environment, err := s.env(ctx, tenant, environmentID)
	if err != nil {
		return nil, err
	}
	if environment.ApplicationID != applicationID {
		return nil, domain.NewError(domain.CodeForbidden, "application mismatch")
	}
	return s.listSecretMetadata(ctx, tenant, environmentID)
}
func (s *Service) DeleteSecret(ctx context.Context, r DeleteSecretRequest) (attachmentsv1.AttachmentSnapshotRef, error) {
	s.defaults()
	e, err := s.env(ctx, r.TenantID, r.EnvironmentID)
	if err != nil {
		return attachmentsv1.AttachmentSnapshotRef{}, err
	}
	var m domain.SecretMetadata
	err = s.Store.Transact(ctx, func(tx Tx) error {
		var ok bool
		m, ok = tx.GetSecret(r.SecretID)
		if !ok {
			return domain.NewError(domain.CodeNotFound, "secret not found")
		}
		if m.TenantID != r.TenantID || m.EnvironmentID != r.EnvironmentID {
			return domain.NewError(domain.CodeForbidden, "cross-tenant secret")
		}
		return nil
	})
	if err != nil {
		return attachmentsv1.AttachmentSnapshotRef{}, err
	}
	if s.Secrets != nil {
		path := strings.TrimSuffix(m.ProviderRef, "/"+m.Name)
		if err = s.Secrets.Delete(ctx, r.TenantID, path, m.Name); err != nil {
			return attachmentsv1.AttachmentSnapshotRef{}, domain.NewError(domain.CodeRetryable, "secret delete failed")
		}
	}
	now := s.Clock.Now()
	var snap domain.AttachmentSnapshot
	err = s.Store.Transact(ctx, func(tx Tx) error {
		current, _ := tx.GetSecret(m.ID)
		expected := current.Version
		current.Deleted = true
		current.Version++
		current.UpdatedAt = now
		if err := tx.PutSecret(current, expected); err != nil {
			return err
		}
		set, _ := tx.GetSecretSet(current.SecretSetID)
		if current.Scope == attachmentsv1.SecretScopeRuntime {
			sv := set.Version
			set.Version++
			set.UpdatedAt = now
			if err := tx.PutSecretSet(set, sv); err != nil {
				return err
			}
		}
		var e2 error
		snap, _, e2 = s.snapshot(tx, e, now)
		if e2 != nil {
			return e2
		}
		if err := tx.PutIdempotency(domain.IdempotencyRecord{TenantID: r.TenantID, Scope: "secret.delete", Key: r.IdempotencyKey, RequestHash: reqHash(r.SecretID), ResourceID: r.SecretID, CreatedAt: now}); err != nil {
			return err
		}
		return s.records(tx, now, r.TenantID, r.ActorID, "attachments.secret.deleted", r.SecretID, map[string]any{"secret_id": r.SecretID, "snapshot_id": snap.Value.SnapshotID})
	})
	if err != nil {
		return attachmentsv1.AttachmentSnapshotRef{}, err
	}
	if m.Scope == attachmentsv1.SecretScopeRuntime {
		_ = s.publish(ctx, snap)
	}
	return attachmentsv1.AttachmentSnapshotRef{SnapshotID: snap.Value.SnapshotID, EnvironmentID: snap.Value.EnvironmentID, Version: snap.Value.Version}, nil
}

// ResolveProjectBuildSecretRefs resolves build-time secret references after
// binding the environment to the application/project identity from the
// verified caller scope. The returned values are provider references, never
// plaintext secret material.
func (s *Service) ResolveProjectBuildSecretRefs(ctx context.Context, tenant, applicationID, env string, phase attachmentsv1.SecretPhase) (map[string]string, error) {
	if strings.TrimSpace(applicationID) == "" {
		return nil, domain.NewError(domain.CodeInvalidArgument, "application required")
	}
	environment, err := s.env(ctx, tenant, env)
	if err != nil {
		return nil, err
	}
	if environment.ApplicationID != applicationID {
		return nil, domain.NewError(domain.CodeForbidden, "application mismatch")
	}
	return s.resolveBuildSecretRefs(ctx, tenant, env, phase)
}

// ResolveBuildSecretRefs is retained for the v1 in-process compatibility
// surface. Deprecated: production adapters must use
// ResolveProjectBuildSecretRefs with a verified project/application scope.
func (s *Service) ResolveBuildSecretRefs(ctx context.Context, tenant, env string, phase attachmentsv1.SecretPhase) (map[string]string, error) {
	if _, err := s.env(ctx, tenant, env); err != nil {
		return nil, err
	}
	return s.resolveBuildSecretRefs(ctx, tenant, env, phase)
}

func (s *Service) resolveBuildSecretRefs(ctx context.Context, tenant, env string, phase attachmentsv1.SecretPhase) (map[string]string, error) {
	if phase != attachmentsv1.SecretPhaseBuild && phase != attachmentsv1.SecretPhaseDetect {
		return nil, domain.NewError(domain.CodeInvalidArgument, "invalid build phase")
	}
	out := map[string]string{}
	err := s.Store.Transact(ctx, func(tx Tx) error {
		set, ok := tx.FindSecretSet(tenant, env)
		if !ok {
			return nil
		}
		for _, m := range tx.ListSecrets(set.ID) {
			if !m.Deleted && m.Scope == attachmentsv1.SecretScopeBuild && m.Phase == phase && (m.ExpiresAt.IsZero() || s.Clock.Now().Before(m.ExpiresAt)) {
				out[m.Name] = m.ProviderRef
			}
		}
		return nil
	})
	return out, err
}

func (s *Service) RegisterServicePlan(ctx context.Context, p domain.ServicePlan, actor string) error {
	s.defaults()
	return s.Store.Transact(ctx, func(tx Tx) error {
		if old, ok := tx.GetPlan(p.ID, p.Version); ok {
			if domain.Hash(old) != domain.Hash(p) {
				return domain.NewError(domain.CodeConflict, "plan version immutable")
			}
			return nil
		}
		if err := tx.PutPlan(p); err != nil {
			return err
		}
		return s.records(tx, s.Clock.Now(), "platform", actor, "attachments.plan.registered", p.ID, map[string]any{"plan_id": p.ID, "version": p.Version, "mapping_version": p.ProviderMappingVersion})
	})
}
func (s *Service) plan(ctx context.Context, id string, v int64) (domain.ServicePlan, error) {
	var p domain.ServicePlan
	err := s.Store.Transact(ctx, func(tx Tx) error {
		var ok bool
		if v > 0 {
			p, ok = tx.GetPlan(id, v)
		} else {
			p, ok = tx.LatestPlan(id)
		}
		if !ok {
			return domain.NewError(domain.CodeNotFound, "service plan not found")
		}
		return nil
	})
	return p, err
}
func (s *Service) GetServiceInstance(ctx context.Context, tenant, id string) (domain.ServiceInstance, error) {
	var v domain.ServiceInstance
	err := s.Store.Transact(ctx, func(tx Tx) error {
		var ok bool
		v, ok = tx.GetInstance(id)
		if !ok {
			return domain.NewError(domain.CodeNotFound, "service not found")
		}
		if v.TenantID != tenant {
			return domain.NewError(domain.CodeForbidden, "cross-tenant service")
		}
		return nil
	})
	return v, err
}
func (s *Service) GetBinding(ctx context.Context, tenant, id string) (domain.ServiceBinding, error) {
	var v domain.ServiceBinding
	err := s.Store.Transact(ctx, func(tx Tx) error {
		var ok bool
		v, ok = tx.GetBinding(id)
		if !ok {
			return domain.NewError(domain.CodeNotFound, "binding not found")
		}
		if v.TenantID != tenant {
			return domain.NewError(domain.CodeForbidden, "cross-tenant binding")
		}
		return nil
	})
	return v, err
}
func providerErr(err error) error {
	if err == nil {
		return nil
	}
	if p, ok := err.(*ProviderError); ok {
		if p.Retryable {
			return domain.NewError(domain.CodeRetryable, p.Code)
		}
		return domain.NewError(domain.CodeConflict, p.Code)
	}
	return domain.NewError(domain.CodeUnavailable, "provider operation failed")
}

func (s *Service) authorizeAllocation(ctx context.Context, r ProvisionServiceRequest, p domain.ServicePlan) error {
	if s.Commerce == nil {
		return domain.NewError(domain.CodeUnavailable, "commercial entitlement service unavailable")
	}
	decision, err := s.Commerce.Check(ctx, commercev1.EntitlementRequest{
		TenantID: r.TenantID,
		Feature:  "attachments.service.provision",
		Resource: string(p.Type) + ":" + p.ID,
		Quantity: 1,
		At:       s.Clock.Now(),
	})
	if err != nil {
		return domain.Wrap(domain.CodeUnavailable, "commercial entitlement unavailable", err)
	}
	if !decision.Allowed {
		return domain.NewError(domain.CodeEntitlementDenied, "managed service allocation is not permitted by the active plan")
	}
	return nil
}

func (s *Service) emitAllocationUsage(ctx context.Context, r ProvisionServiceRequest, p domain.ServicePlan, instance domain.ServiceInstance, now time.Time) error {
	if s.Usage == nil {
		return domain.NewError(domain.CodeUnavailable, "commercial usage sink unavailable")
	}
	meter := commercev1.MeterDatabasePlanSeconds
	if p.Type == attachmentsv1.ServiceS3 {
		meter = commercev1.MeterObjectStorageGiBHours
	}
	if err := s.Usage.Append(ctx, commercev1.UsageEvent{
		TenantID:       r.TenantID,
		ResourceType:   "managed_service",
		ResourceID:     instance.ID,
		Meter:          meter,
		Kind:           commercev1.UsageStandard,
		Quantity:       1,
		IdempotencyKey: "attachments.allocation:" + instance.ID,
		OccurredAt:     now,
		WindowStart:    now,
		WindowEnd:      now.Add(time.Second),
		Metadata:       map[string]string{"plan_id": p.ID, "service_type": string(p.Type)},
	}); err != nil {
		return domain.Wrap(domain.CodeRetryable, "commercial usage emission failed", err)
	}
	return nil
}

func (s *Service) ProvisionService(ctx context.Context, r ProvisionServiceRequest) (domain.ServiceInstance, error) {
	s.defaults()
	if strings.TrimSpace(r.TenantID) == "" || strings.TrimSpace(r.Name) == "" {
		return domain.ServiceInstance{}, domain.NewError(domain.CodeInvalidArgument, "tenant and service name required")
	}
	// Service instances may be tenant-level and bound to an environment later.
	// ApplicationID/EnvironmentID are optional placement hints retained for backward compatibility.
	if r.EnvironmentID != "" {
		e, envErr := s.env(ctx, r.TenantID, r.EnvironmentID)
		if envErr != nil {
			return domain.ServiceInstance{}, envErr
		}
		if e.ApplicationID != r.ApplicationID {
			return domain.ServiceInstance{}, domain.NewError(domain.CodeForbidden, "application mismatch")
		}
	} else if r.ApplicationID != "" {
		return domain.ServiceInstance{}, domain.NewError(domain.CodeInvalidArgument, "environment required when application is specified")
	}
	p, err := s.plan(ctx, r.PlanID, r.PlanVersion)
	if err != nil {
		return domain.ServiceInstance{}, err
	}
	if !p.Enabled {
		return domain.ServiceInstance{}, domain.NewError(domain.CodeConflict, "plan disabled")
	}
	if err := s.authorizeAllocation(ctx, r, p); err != nil {
		return domain.ServiceInstance{}, err
	}
	hash := reqHash(struct {
		T, A, E, N, P string
		V             int64
	}{r.TenantID, r.ApplicationID, r.EnvironmentID, strings.ToLower(r.Name), p.ID, p.Version})
	now := s.Clock.Now()
	var instance domain.ServiceInstance
	created := false
	err = s.Store.Transact(ctx, func(tx Tx) error {
		created = false
		if id, ok, e2 := idem(tx, r.TenantID, "service.provision", r.IdempotencyKey, hash); e2 != nil {
			return e2
		} else if ok {
			instance, _ = tx.GetInstance(id)
			return nil
		}
		if old, ok := tx.FindInstance(r.TenantID, r.EnvironmentID, strings.ToLower(r.Name)); ok {
			instance = old
			return tx.PutIdempotency(domain.IdempotencyRecord{TenantID: r.TenantID, Scope: "service.provision", Key: r.IdempotencyKey, RequestHash: hash, ResourceID: old.ID, CreatedAt: now})
		}
		instance = domain.ServiceInstance{ID: s.IDs.New("svc"), TenantID: r.TenantID, ApplicationID: r.ApplicationID, EnvironmentID: r.EnvironmentID, Name: strings.ToLower(r.Name), PlanID: p.ID, PlanVersion: p.Version, Type: p.Type, State: attachmentsv1.ServiceRequested, ProviderOperationKey: "provision:" + r.TenantID + ":" + r.IdempotencyKey, Version: 1, CreatedAt: now, UpdatedAt: now}
		if err := tx.PutInstance(instance, 0); err != nil {
			return err
		}
		if err := tx.PutIdempotency(domain.IdempotencyRecord{TenantID: r.TenantID, Scope: "service.provision", Key: r.IdempotencyKey, RequestHash: hash, ResourceID: instance.ID, CreatedAt: now}); err != nil {
			return err
		}
		created = true
		return s.records(tx, now, r.TenantID, r.ActorID, "attachments.service.requested", instance.ID, map[string]any{"instance_id": instance.ID, "plan_id": p.ID, "type": p.Type})
	})
	if err != nil || !created {
		return instance, err
	}
	if s.Provider == nil {
		return instance, domain.NewError(domain.CodeUnavailable, "provider unavailable")
	}
	labels := map[string]string{"platform.tenant_id": r.TenantID, "platform.instance_id": instance.ID}
	if r.ApplicationID != "" {
		labels["platform.application_id"] = r.ApplicationID
	}
	if r.EnvironmentID != "" {
		labels["platform.environment_id"] = r.EnvironmentID
	}
	res, pe := s.Provider.EnsureInstance(ctx, p, instance, labels)
	now = s.Clock.Now()
	err = s.Store.Transact(ctx, func(tx Tx) error {
		cur, _ := tx.GetInstance(instance.ID)
		expected := cur.Version
		if pe != nil {
			cur.State = attachmentsv1.ServiceFailedRetryable
			cur.FailureCode = "PROVIDER_UNAVAILABLE"
			if x, ok := pe.(*ProviderError); ok {
				cur.FailureCode = x.Code
				if !x.Retryable {
					cur.State = attachmentsv1.ServiceFailedFinal
				}
			}
			cur.FailureMessage = "provider operation failed"
		} else if res.FailedFinal {
			cur.State = attachmentsv1.ServiceFailedFinal
			cur.FailureCode = res.FailureCode
			cur.FailureMessage = "provider rejected request"
		} else {
			cur.ProviderID = res.ProviderID
			cur.ProviderEndpoint = res.Endpoint
			if res.Ready {
				cur.State = attachmentsv1.ServiceReady
			} else {
				cur.State = attachmentsv1.ServiceProvisioning
			}
		}
		cur.Version++
		cur.UpdatedAt = now
		if err := tx.PutInstance(cur, expected); err != nil {
			return err
		}
		instance = cur
		return s.records(tx, now, r.TenantID, r.ActorID, "attachments.service.state", cur.ID, map[string]any{"instance_id": cur.ID, "state": cur.State, "failure_code": cur.FailureCode})
	})
	if pe != nil {
		return instance, providerErr(pe)
	}
	if err == nil && !res.FailedFinal {
		err = s.emitAllocationUsage(ctx, r, p, instance, now)
	}
	return instance, err
}
func (s *Service) ReconcileService(ctx context.Context, tenant, id, actor string) (domain.ServiceInstance, error) {
	s.defaults()
	v, err := s.GetServiceInstance(ctx, tenant, id)
	if err != nil {
		return v, err
	}
	p, err := s.plan(ctx, v.PlanID, v.PlanVersion)
	if err != nil {
		return v, err
	}
	var res ProviderInstanceResult
	if v.ProviderID == "" {
		var ok bool
		res, ok, err = s.Provider.FindInstance(ctx, p, v)
		if err == nil && !ok {
			return v, domain.NewError(domain.CodeRetryable, "resource not observed")
		}
	} else {
		res, err = s.Provider.GetInstance(ctx, p, v)
	}
	if err != nil {
		return v, providerErr(err)
	}
	now := s.Clock.Now()
	err = s.Store.Transact(ctx, func(tx Tx) error {
		cur, _ := tx.GetInstance(v.ID)
		expected := cur.Version
		if cur.ProviderID != "" && cur.ProviderID != res.ProviderID {
			return domain.NewError(domain.CodeConflict, "provider identity immutable")
		}
		cur.ProviderID = res.ProviderID
		cur.ProviderEndpoint = res.Endpoint
		if res.FailedFinal {
			cur.State = attachmentsv1.ServiceFailedFinal
			cur.FailureCode = res.FailureCode
			cur.FailureMessage = "provider final failure"
		} else if res.Ready {
			cur.State = attachmentsv1.ServiceReady
		} else {
			cur.State = attachmentsv1.ServiceProvisioning
		}
		cur.Version++
		cur.UpdatedAt = now
		if err := tx.PutInstance(cur, expected); err != nil {
			return err
		}
		v = cur
		return s.records(tx, now, tenant, actor, "attachments.service.reconciled", cur.ID, map[string]any{"instance_id": cur.ID, "state": cur.State})
	})
	return v, err
}
func (s *Service) BindService(ctx context.Context, r BindServiceRequest) (domain.ServiceBinding, attachmentsv1.AttachmentSnapshotRef, error) {
	s.defaults()
	e, err := s.env(ctx, r.TenantID, r.EnvironmentID)
	if err != nil {
		return domain.ServiceBinding{}, attachmentsv1.AttachmentSnapshotRef{}, err
	}
	if e.ApplicationID != r.ApplicationID {
		return domain.ServiceBinding{}, attachmentsv1.AttachmentSnapshotRef{}, domain.NewError(domain.CodeForbidden, "application mismatch")
	}
	instance, err := s.GetServiceInstance(ctx, r.TenantID, r.InstanceID)
	if err != nil {
		return domain.ServiceBinding{}, attachmentsv1.AttachmentSnapshotRef{}, err
	}
	if instance.State != attachmentsv1.ServiceReady {
		return domain.ServiceBinding{}, attachmentsv1.AttachmentSnapshotRef{}, domain.NewError(domain.CodeConflict, "service not ready")
	}
	p, err := s.plan(ctx, instance.PlanID, instance.PlanVersion)
	if err != nil {
		return domain.ServiceBinding{}, attachmentsv1.AttachmentSnapshotRef{}, err
	}
	allowed := map[string]bool{}
	for _, c := range p.Capabilities {
		allowed[c] = true
	}
	caps := domain.CanonicalStrings(r.Capabilities)
	for _, c := range caps {
		if !allowed[c] {
			return domain.ServiceBinding{}, attachmentsv1.AttachmentSnapshotRef{}, domain.NewError(domain.CodeForbidden, "capability not allowed")
		}
	}
	hash := reqHash(struct {
		T, E, I string
		C       []string
	}{r.TenantID, r.EnvironmentID, r.InstanceID, caps})
	now := s.Clock.Now()
	var b domain.ServiceBinding
	created := false
	err = s.Store.Transact(ctx, func(tx Tx) error {
		created = false
		if id, ok, e2 := idem(tx, r.TenantID, "service.bind", r.IdempotencyKey, hash); e2 != nil {
			return e2
		} else if ok {
			b, _ = tx.GetBinding(id)
			return nil
		}
		if old, ok := tx.FindBinding(r.TenantID, r.EnvironmentID, r.InstanceID); ok {
			b = old
			return tx.PutIdempotency(domain.IdempotencyRecord{TenantID: r.TenantID, Scope: "service.bind", Key: r.IdempotencyKey, RequestHash: hash, ResourceID: old.ID, CreatedAt: now})
		}
		b = domain.ServiceBinding{ID: s.IDs.New("bind"), TenantID: r.TenantID, ApplicationID: r.ApplicationID, EnvironmentID: r.EnvironmentID, InstanceID: r.InstanceID, State: attachmentsv1.BindingRequested, Capabilities: caps, Version: 1, CreatedAt: now, UpdatedAt: now}
		if err := tx.PutBinding(b, 0); err != nil {
			return err
		}
		if err := tx.PutIdempotency(domain.IdempotencyRecord{TenantID: r.TenantID, Scope: "service.bind", Key: r.IdempotencyKey, RequestHash: hash, ResourceID: b.ID, CreatedAt: now}); err != nil {
			return err
		}
		created = true
		return s.records(tx, now, r.TenantID, r.ActorID, "attachments.binding.requested", b.ID, map[string]any{"binding_id": b.ID, "instance_id": b.InstanceID, "capabilities": b.Capabilities})
	})
	if err != nil {
		return b, attachmentsv1.AttachmentSnapshotRef{}, err
	}
	if !created && b.State == attachmentsv1.BindingActive {
		var snap domain.AttachmentSnapshot
		_ = s.Store.Transact(ctx, func(tx Tx) error { snap, _ = tx.LatestSnapshot(r.TenantID, r.EnvironmentID); return nil })
		return b, attachmentsv1.AttachmentSnapshotRef{SnapshotID: snap.Value.SnapshotID, EnvironmentID: snap.Value.EnvironmentID, Version: snap.Value.Version}, nil
	}
	cred, err := s.Provider.IssueCredential(ctx, p, instance, b)
	if err != nil {
		return b, attachmentsv1.AttachmentSnapshotRef{}, providerErr(err)
	}
	path := fmt.Sprintf("tenants/%s/apps/%s/%s/bindings/%s", r.TenantID, r.ApplicationID, r.EnvironmentID, b.ID)
	for name, value := range cred.Values {
		if _, err = s.Secrets.Write(ctx, r.TenantID, path, name, value, time.Time{}); err != nil {
			return b, attachmentsv1.AttachmentSnapshotRef{}, domain.NewError(domain.CodeRetryable, "credential storage failed")
		}
	}
	now = s.Clock.Now()
	var snap domain.AttachmentSnapshot
	err = s.Store.Transact(ctx, func(tx Tx) error {
		cur, _ := tx.GetBinding(b.ID)
		expected := cur.Version
		cur.State = attachmentsv1.BindingActive
		cur.ProviderCredentialID = cred.CredentialID
		cur.CredentialRef = path + "#" + cred.CredentialID
		cur.Version++
		cur.UpdatedAt = now
		if err := tx.PutBinding(cur, expected); err != nil {
			return err
		}
		var e2 error
		snap, _, e2 = s.snapshot(tx, e, now)
		if e2 != nil {
			return e2
		}
		b = cur
		return s.records(tx, now, r.TenantID, r.ActorID, "attachments.binding.active", cur.ID, map[string]any{"binding_id": cur.ID, "instance_id": cur.InstanceID, "snapshot_id": snap.Value.SnapshotID})
	})
	if err != nil {
		return b, attachmentsv1.AttachmentSnapshotRef{}, err
	}
	if err = s.publish(ctx, snap); err != nil {
		return b, attachmentsv1.AttachmentSnapshotRef{}, err
	}
	if s.Logger != nil {
		s.Logger.Log(ctx, "attachments.binding.active", map[string]string{"binding_id": b.ID, "credentials": "[REDACTED]"})
	}
	return b, attachmentsv1.AttachmentSnapshotRef{SnapshotID: snap.Value.SnapshotID, EnvironmentID: snap.Value.EnvironmentID, Version: snap.Value.Version}, nil
}
func (s *Service) RotateBinding(ctx context.Context, r RotateBindingRequest) (domain.ServiceBinding, attachmentsv1.AttachmentSnapshotRef, error) {
	s.defaults()
	b, err := s.GetBinding(ctx, r.TenantID, r.BindingID)
	if err != nil {
		return b, attachmentsv1.AttachmentSnapshotRef{}, err
	}
	if b.State != attachmentsv1.BindingActive {
		return b, attachmentsv1.AttachmentSnapshotRef{}, domain.NewError(domain.CodeConflict, "binding not active")
	}
	instance, _ := s.GetServiceInstance(ctx, r.TenantID, b.InstanceID)
	p, _ := s.plan(ctx, instance.PlanID, instance.PlanVersion)
	cred, err := s.Provider.IssueCredential(ctx, p, instance, b)
	if err != nil {
		return b, attachmentsv1.AttachmentSnapshotRef{}, providerErr(err)
	}
	path := fmt.Sprintf("tenants/%s/apps/%s/%s/bindings/%s/rotations/%s", b.TenantID, b.ApplicationID, b.EnvironmentID, b.ID, cred.CredentialID)
	for name, value := range cred.Values {
		if _, err = s.Secrets.Write(ctx, b.TenantID, path, name, value, time.Time{}); err != nil {
			return b, attachmentsv1.AttachmentSnapshotRef{}, domain.NewError(domain.CodeRetryable, "credential storage failed")
		}
	}
	oldID := b.ProviderCredentialID
	now := s.Clock.Now()
	e, _ := s.env(ctx, b.TenantID, b.EnvironmentID)
	var snap domain.AttachmentSnapshot
	err = s.Store.Transact(ctx, func(tx Tx) error {
		cur, _ := tx.GetBinding(b.ID)
		expected := cur.Version
		cur.PreviousCredentialID = cur.ProviderCredentialID
		cur.PreviousCredentialRef = cur.CredentialRef
		cur.ProviderCredentialID = cred.CredentialID
		cur.CredentialRef = path + "#" + cred.CredentialID
		cur.State = attachmentsv1.BindingRotating
		cur.Version++
		cur.UpdatedAt = now
		if err := tx.PutBinding(cur, expected); err != nil {
			return err
		}
		var e2 error
		snap, _, e2 = s.snapshot(tx, e, now)
		if e2 != nil {
			return e2
		}
		cur.SnapshotID = snap.Value.SnapshotID
		expected = cur.Version
		cur.Version++
		if err := tx.PutBinding(cur, expected); err != nil {
			return err
		}
		b = cur
		return s.records(tx, now, r.TenantID, r.ActorID, "attachments.binding.rotation.started", cur.ID, map[string]any{"binding_id": cur.ID, "snapshot_id": snap.Value.SnapshotID})
	})
	if err != nil {
		return b, attachmentsv1.AttachmentSnapshotRef{}, err
	}
	if err = s.publish(ctx, snap); err != nil {
		return b, attachmentsv1.AttachmentSnapshotRef{}, err
	}
	if err = s.Provider.RevokeCredential(ctx, p, instance, oldID); err != nil {
		return b, attachmentsv1.AttachmentSnapshotRef{}, providerErr(err)
	}
	err = s.Store.Transact(ctx, func(tx Tx) error {
		cur, _ := tx.GetBinding(b.ID)
		expected := cur.Version
		cur.State = attachmentsv1.BindingActive
		cur.PreviousCredentialID = ""
		cur.PreviousCredentialRef = ""
		cur.Version++
		cur.UpdatedAt = s.Clock.Now()
		if err := tx.PutBinding(cur, expected); err != nil {
			return err
		}
		b = cur
		return s.records(tx, s.Clock.Now(), r.TenantID, r.ActorID, "attachments.binding.rotation.completed", cur.ID, map[string]any{"binding_id": cur.ID, "snapshot_id": snap.Value.SnapshotID})
	})
	return b, attachmentsv1.AttachmentSnapshotRef{SnapshotID: snap.Value.SnapshotID, EnvironmentID: snap.Value.EnvironmentID, Version: snap.Value.Version}, err
}
func (s *Service) RevokeBinding(ctx context.Context, r RevokeBindingRequest) (domain.ServiceBinding, attachmentsv1.AttachmentSnapshotRef, error) {
	s.defaults()
	b, err := s.GetBinding(ctx, r.TenantID, r.BindingID)
	if err != nil {
		return b, attachmentsv1.AttachmentSnapshotRef{}, err
	}
	if b.State == attachmentsv1.BindingRevoked {
		var snap domain.AttachmentSnapshot
		_ = s.Store.Transact(ctx, func(tx Tx) error { snap, _ = tx.LatestSnapshot(r.TenantID, b.EnvironmentID); return nil })
		return b, attachmentsv1.AttachmentSnapshotRef{SnapshotID: snap.Value.SnapshotID, EnvironmentID: snap.Value.EnvironmentID, Version: snap.Value.Version}, nil
	}
	instance, _ := s.GetServiceInstance(ctx, r.TenantID, b.InstanceID)
	p, _ := s.plan(ctx, instance.PlanID, instance.PlanVersion)
	if b.ProviderCredentialID != "" {
		if err = s.Provider.RevokeCredential(ctx, p, instance, b.ProviderCredentialID); err != nil {
			return b, attachmentsv1.AttachmentSnapshotRef{}, providerErr(err)
		}
	}
	if s.Secrets != nil && b.CredentialRef != "" {
		_ = s.Secrets.Delete(ctx, r.TenantID, strings.Split(b.CredentialRef, "#")[0], "*")
	}
	e, _ := s.env(ctx, b.TenantID, b.EnvironmentID)
	now := s.Clock.Now()
	var snap domain.AttachmentSnapshot
	err = s.Store.Transact(ctx, func(tx Tx) error {
		cur, _ := tx.GetBinding(b.ID)
		expected := cur.Version
		cur.State = attachmentsv1.BindingRevoked
		cur.ProviderCredentialID = ""
		cur.CredentialRef = ""
		cur.Version++
		cur.UpdatedAt = now
		if err := tx.PutBinding(cur, expected); err != nil {
			return err
		}
		var e2 error
		snap, _, e2 = s.snapshot(tx, e, now)
		if e2 != nil {
			return e2
		}
		b = cur
		return s.records(tx, now, r.TenantID, r.ActorID, "attachments.binding.revoked", cur.ID, map[string]any{"binding_id": cur.ID, "snapshot_id": snap.Value.SnapshotID})
	})
	if err == nil {
		err = s.publish(ctx, snap)
	}
	return b, attachmentsv1.AttachmentSnapshotRef{SnapshotID: snap.Value.SnapshotID, EnvironmentID: snap.Value.EnvironmentID, Version: snap.Value.Version}, err
}
func (s *Service) DeleteApplicationAttachments(ctx context.Context, tenant, app, actor string) error {
	var all []domain.ServiceBinding
	if err := s.Store.Transact(ctx, func(tx Tx) error { all = tx.ListAppBindings(tenant, app); return nil }); err != nil {
		return err
	}
	for _, b := range all {
		if b.State != attachmentsv1.BindingRevoked {
			if _, _, err := s.RevokeBinding(ctx, RevokeBindingRequest{TenantID: tenant, BindingID: b.ID, ActorID: actor, IdempotencyKey: "app-delete:" + b.ID}); err != nil {
				return err
			}
		}
	}
	return nil
}
func (s *Service) PurgeService(ctx context.Context, r PurgeServiceRequest) (domain.ServiceInstance, error) {
	s.defaults()
	v, err := s.GetServiceInstance(ctx, r.TenantID, r.InstanceID)
	if err != nil {
		return v, err
	}
	if v.State == attachmentsv1.ServiceDeleted {
		return v, nil
	}
	p, err := s.plan(ctx, v.PlanID, v.PlanVersion)
	if err != nil {
		return v, err
	}
	destroyPlan, err := domain.NewProviderResourceDestroyPlan(v, p)
	if err != nil {
		return v, err
	}
	actualPlanHash := destroyPlan.PlanHash()
	if r.ApprovalRef == "" || s.Approvals == nil {
		return v, domain.NewError(domain.CodeApprovalRequired, "purge approval required")
	}
	if r.PlanHash != actualPlanHash {
		return v, domain.NewError(domain.CodeForbidden, "destructive plan hash mismatch")
	}
	if err = s.Approvals.Verify(ctx, r.ApprovalRef, ApprovalBinding{TenantID: r.TenantID, ActorID: r.ActorID, TargetID: r.InstanceID, PlanHash: actualPlanHash}); err != nil {
		return v, domain.NewError(domain.CodeForbidden, "approval mismatch")
	}
	now := s.Clock.Now()
	err = s.Store.Transact(ctx, func(tx Tx) error {
		cur, _ := tx.GetInstance(v.ID)
		expected := cur.Version
		cur.State = attachmentsv1.ServiceDeleting
		cur.Version++
		cur.UpdatedAt = now
		if err := tx.PutInstance(cur, expected); err != nil {
			return err
		}
		v = cur
		return s.records(tx, now, r.TenantID, r.ActorID, "attachments.service.purge.started", cur.ID, map[string]any{"instance_id": cur.ID, "approval_ref": r.ApprovalRef, "plan_hash": actualPlanHash})
	})
	if err != nil {
		return v, err
	}
	if p.BackupBeforePurge {
		backup, e := s.Provider.CreateBackup(ctx, p, v)
		if e != nil {
			return v, providerErr(e)
		}
		err = s.Store.Transact(ctx, func(tx Tx) error {
			cur, _ := tx.GetInstance(v.ID)
			expected := cur.Version
			cur.FinalBackupID = backup
			cur.State = attachmentsv1.ServiceRetainedBackup
			cur.Version++
			if err := tx.PutInstance(cur, expected); err != nil {
				return err
			}
			v = cur
			return s.records(tx, s.Clock.Now(), r.TenantID, r.ActorID, "attachments.service.backup.retained", cur.ID, map[string]any{"instance_id": cur.ID, "backup_id": backup})
		})
		if err != nil {
			return v, err
		}
	}
	if err = s.Provider.DeleteInstance(ctx, p, v); err != nil {
		return v, providerErr(err)
	}
	err = s.Store.Transact(ctx, func(tx Tx) error {
		cur, _ := tx.GetInstance(v.ID)
		expected := cur.Version
		cur.State = attachmentsv1.ServiceDeleted
		cur.Version++
		cur.UpdatedAt = s.Clock.Now()
		if err := tx.PutInstance(cur, expected); err != nil {
			return err
		}
		v = cur
		return s.records(tx, s.Clock.Now(), r.TenantID, r.ActorID, "attachments.service.purged", cur.ID, map[string]any{"instance_id": cur.ID, "state": cur.State, "final_backup_id": cur.FinalBackupID})
	})
	return v, err
}

var reservedDomains = map[string]bool{"api": true, "console": true, "git": true, "mcp": true, "admin": true, "www": true, "status": true}

func (s *Service) generated(tenant, app, env, name string) (string, error) {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		name = "app"
	}
	if reservedDomains[name] {
		return "", domain.NewError(domain.CodeConflict, "reserved domain")
	}
	if !domain.ValidDNSLabel(name) {
		return "", domain.NewError(domain.CodeInvalidArgument, "invalid domain label")
	}
	return domain.CanonicalHostname(fmt.Sprintf("%s-%s.%s", name, domain.Hash(struct{ T, A, E string }{tenant, app, env})[:10], s.DefaultDomain))
}
func (s *Service) CreateGeneratedDomain(ctx context.Context, r GeneratedDomainRequest) (domain.DomainClaim, error) {
	s.defaults()
	e, err := s.env(ctx, r.TenantID, r.EnvironmentID)
	if err != nil {
		return domain.DomainClaim{}, err
	}
	if e.ApplicationID != r.ApplicationID {
		return domain.DomainClaim{}, domain.NewError(domain.CodeForbidden, "application mismatch")
	}
	host, err := s.generated(r.TenantID, r.ApplicationID, r.EnvironmentID, r.PreferredName)
	if err != nil {
		return domain.DomainClaim{}, err
	}
	hash := reqHash(host)
	now := s.Clock.Now()
	var c domain.DomainClaim
	created := false
	err = s.Store.Transact(ctx, func(tx Tx) error {
		created = false
		if id, ok, e2 := idem(tx, r.TenantID, "domain.generated", r.IdempotencyKey, hash); e2 != nil {
			return e2
		} else if ok {
			c, _ = tx.GetClaim(id)
			return nil
		}
		if old, ok := tx.FindClaim(host); ok && old.State != attachmentsv1.DomainReleased {
			return domain.NewError(domain.CodeConflict, "domain claimed")
		}
		c = domain.DomainClaim{ID: s.IDs.New("dom"), TenantID: r.TenantID, ApplicationID: r.ApplicationID, EnvironmentID: r.EnvironmentID, Hostname: host, Generated: true, State: attachmentsv1.DomainVerified, Version: 1, CreatedAt: now, UpdatedAt: now}
		if err := tx.PutClaim(c, 0); err != nil {
			return err
		}
		if err := tx.PutIdempotency(domain.IdempotencyRecord{TenantID: r.TenantID, Scope: "domain.generated", Key: r.IdempotencyKey, RequestHash: hash, ResourceID: c.ID, CreatedAt: now}); err != nil {
			return err
		}
		created = true
		return s.records(tx, now, r.TenantID, r.ActorID, "attachments.domain.generated", c.ID, map[string]any{"claim_id": c.ID, "hostname": c.Hostname})
	})
	if err != nil || !created {
		return c, err
	}
	return s.ensureCertificate(ctx, c, r.ActorID)
}
func (s *Service) ClaimCustomDomain(ctx context.Context, r ClaimDomainRequest) (domain.DomainClaim, error) {
	s.defaults()
	e, err := s.env(ctx, r.TenantID, r.EnvironmentID)
	if err != nil {
		return domain.DomainClaim{}, err
	}
	if e.ApplicationID != r.ApplicationID {
		return domain.DomainClaim{}, domain.NewError(domain.CodeForbidden, "application mismatch")
	}
	host, err := domain.CanonicalHostname(r.Hostname)
	if err != nil {
		return domain.DomainClaim{}, err
	}
	hash := reqHash(host)
	now := s.Clock.Now()
	var c domain.DomainClaim
	err = s.Store.Transact(ctx, func(tx Tx) error {
		if id, ok, e2 := idem(tx, r.TenantID, "domain.claim", r.IdempotencyKey, hash); e2 != nil {
			return e2
		} else if ok {
			c, _ = tx.GetClaim(id)
			return nil
		}
		if old, ok := tx.FindClaim(host); ok && old.State != attachmentsv1.DomainReleased {
			return domain.NewError(domain.CodeConflict, "domain claimed or quarantined")
		}
		challenge := "platform-verification=" + domain.Hash(struct{ T, E, H, N string }{r.TenantID, r.EnvironmentID, host, s.IDs.New("challenge")})[:32]
		c = domain.DomainClaim{ID: s.IDs.New("dom"), TenantID: r.TenantID, ApplicationID: r.ApplicationID, EnvironmentID: r.EnvironmentID, Hostname: host, State: attachmentsv1.DomainAwaitingVerification, ChallengeName: "_paas-verification." + host, ChallengeValue: challenge, Version: 1, CreatedAt: now, UpdatedAt: now}
		if err := tx.PutClaim(c, 0); err != nil {
			return err
		}
		if err := tx.PutIdempotency(domain.IdempotencyRecord{TenantID: r.TenantID, Scope: "domain.claim", Key: r.IdempotencyKey, RequestHash: hash, ResourceID: c.ID, CreatedAt: now}); err != nil {
			return err
		}
		return s.records(tx, now, r.TenantID, r.ActorID, "attachments.domain.claimed", c.ID, map[string]any{"claim_id": c.ID, "hostname": c.Hostname, "challenge_name": c.ChallengeName})
	})
	return c, err
}
func (s *Service) GetDomainClaim(ctx context.Context, tenant, id string) (domain.DomainClaim, error) {
	var c domain.DomainClaim
	err := s.Store.Transact(ctx, func(tx Tx) error {
		var ok bool
		c, ok = tx.GetClaim(id)
		if !ok {
			return domain.NewError(domain.CodeNotFound, "domain not found")
		}
		if c.TenantID != tenant {
			return domain.NewError(domain.CodeForbidden, "cross-tenant domain")
		}
		return nil
	})
	return c, err
}
func (s *Service) VerifyDomain(ctx context.Context, r VerifyDomainRequest) (domain.DomainClaim, error) {
	s.defaults()
	c, err := s.GetDomainClaim(ctx, r.TenantID, r.ClaimID)
	if err != nil {
		return c, err
	}
	values, err := s.DNS.ReadTXT(ctx, c.ChallengeName)
	if err != nil {
		return c, domain.NewError(domain.CodeRetryable, "DNS unavailable")
	}
	found := false
	for _, v := range values {
		if strings.TrimSpace(v) == c.ChallengeValue {
			found = true
		}
	}
	if !found {
		return c, domain.NewError(domain.CodeInvalidArgument, "challenge not observed")
	}
	now := s.Clock.Now()
	obsErr := error(nil)
	err = s.Store.Transact(ctx, func(tx Tx) error {
		cur, _ := tx.GetClaim(c.ID)
		expected := cur.Version
		h := domain.DNSHash(values)
		if cur.FirstObservationHash == "" {
			cur.FirstObservationHash = h
			cur.FirstObservedAt = now
			cur.Version++
			cur.UpdatedAt = now
		} else if h != cur.FirstObservationHash {
			cur.State = attachmentsv1.DomainVerificationFailed
			cur.FailureCode = "DNS_REBINDING"
			cur.Version++
			cur.UpdatedAt = now
			obsErr = domain.NewError(domain.CodeConflict, "DNS answer changed")
		} else if now.Sub(cur.FirstObservedAt) < s.DNSObservationDelay {
			obsErr = domain.NewError(domain.CodeRetryable, "second observation not due")
		} else {
			cur.State = attachmentsv1.DomainVerified
			cur.Version++
			cur.UpdatedAt = now
		}
		if cur.Version != expected {
			if err := tx.PutClaim(cur, expected); err != nil {
				return err
			}
			c = cur
		}
		return nil
	})
	if err != nil {
		return c, err
	}
	if obsErr != nil {
		return c, obsErr
	}
	if c.State == attachmentsv1.DomainVerified {
		return s.ensureCertificate(ctx, c, r.ActorID)
	}
	return c, nil
}
func (s *Service) ensureCertificate(ctx context.Context, c domain.DomainClaim, actor string) (domain.DomainClaim, error) {
	res, err := s.Certificates.Ensure(ctx, c.TenantID, c.Hostname)
	if err != nil {
		return c, domain.NewError(domain.CodeRetryable, "certificate request failed")
	}
	now := s.Clock.Now()
	var snap domain.AttachmentSnapshot
	err = s.Store.Transact(ctx, func(tx Tx) error {
		cur, _ := tx.GetClaim(c.ID)
		expected := cur.Version
		cur.CertificateID = res.CertificateID
		cur.State = attachmentsv1.DomainTLSPending
		if res.Status == CertificateReady {
			cur.State = attachmentsv1.DomainActive
		} else if res.Status == CertificateFailed {
			cur.FailureCode = res.FailureCode
		}
		cur.Version++
		cur.UpdatedAt = now
		if err := tx.PutClaim(cur, expected); err != nil {
			return err
		}
		c = cur
		if cur.State == attachmentsv1.DomainActive {
			e, _ := s.env(ctx, cur.TenantID, cur.EnvironmentID)
			var e2 error
			snap, _, e2 = s.snapshot(tx, e, now)
			if e2 != nil {
				return e2
			}
		}
		return s.records(tx, now, cur.TenantID, actor, "attachments.domain.certificate", cur.ID, map[string]any{"claim_id": cur.ID, "state": cur.State, "certificate_id": cur.CertificateID})
	})
	if err == nil && c.State == attachmentsv1.DomainActive {
		err = s.publish(ctx, snap)
	}
	return c, err
}
func (s *Service) ReconcileCertificate(ctx context.Context, tenant, id, actor string) (domain.DomainClaim, error) {
	c, err := s.GetDomainClaim(ctx, tenant, id)
	if err != nil || c.State != attachmentsv1.DomainTLSPending {
		return c, err
	}
	res, err := s.Certificates.Status(ctx, c.CertificateID)
	if err != nil {
		return c, domain.NewError(domain.CodeRetryable, "certificate status failed")
	}
	now := s.Clock.Now()
	var snap domain.AttachmentSnapshot
	err = s.Store.Transact(ctx, func(tx Tx) error {
		cur, _ := tx.GetClaim(c.ID)
		expected := cur.Version
		if res.Status == CertificateReady {
			cur.State = attachmentsv1.DomainActive
		} else if res.Status == CertificateFailed {
			cur.FailureCode = res.FailureCode
		}
		cur.Version++
		cur.UpdatedAt = now
		if err := tx.PutClaim(cur, expected); err != nil {
			return err
		}
		c = cur
		if cur.State == attachmentsv1.DomainActive {
			e, _ := s.env(ctx, cur.TenantID, cur.EnvironmentID)
			var e2 error
			snap, _, e2 = s.snapshot(tx, e, now)
			return e2
		}
		return nil
	})
	if err == nil && c.State == attachmentsv1.DomainActive {
		err = s.publish(ctx, snap)
	}
	return c, err
}
func (s *Service) DeleteDomain(ctx context.Context, r DeleteDomainRequest) (domain.DomainClaim, error) {
	s.defaults()
	c, err := s.GetDomainClaim(ctx, r.TenantID, r.ClaimID)
	if err != nil {
		return c, err
	}
	e, _ := s.env(ctx, c.TenantID, c.EnvironmentID)
	now := s.Clock.Now()
	var snap domain.AttachmentSnapshot
	err = s.Store.Transact(ctx, func(tx Tx) error {
		cur, _ := tx.GetClaim(c.ID)
		expected := cur.Version
		cur.State = attachmentsv1.DomainQuarantined
		cur.QuarantineUntil = now.Add(s.DomainQuarantine)
		cur.Version++
		cur.UpdatedAt = now
		if err := tx.PutClaim(cur, expected); err != nil {
			return err
		}
		c = cur
		var e2 error
		snap, _, e2 = s.snapshot(tx, e, now)
		if e2 != nil {
			return e2
		}
		return s.records(tx, now, r.TenantID, r.ActorID, "attachments.domain.quarantined", cur.ID, map[string]any{"claim_id": cur.ID, "hostname": cur.Hostname, "quarantine_until": cur.QuarantineUntil})
	})
	if err == nil {
		err = s.publish(ctx, snap)
	}
	return c, err
}
func (s *Service) ReleaseDomain(ctx context.Context, tenant, id, actor string) (domain.DomainClaim, error) {
	c, err := s.GetDomainClaim(ctx, tenant, id)
	if err != nil {
		return c, err
	}
	if c.State != attachmentsv1.DomainQuarantined || s.Clock.Now().Before(c.QuarantineUntil) {
		return c, domain.NewError(domain.CodeConflict, "quarantine active")
	}
	err = s.Store.Transact(ctx, func(tx Tx) error {
		cur, _ := tx.GetClaim(id)
		expected := cur.Version
		cur.State = attachmentsv1.DomainReleased
		cur.Version++
		cur.UpdatedAt = s.Clock.Now()
		if err := tx.PutClaim(cur, expected); err != nil {
			return err
		}
		c = cur
		return s.records(tx, s.Clock.Now(), tenant, actor, "attachments.domain.released", id, map[string]any{"claim_id": id, "hostname": cur.Hostname})
	})
	return c, err
}
func (s *Service) RouteForEnvironment(ctx context.Context, tenant, env string) (string, error) {
	if _, err := s.env(ctx, tenant, env); err != nil {
		return "", err
	}
	host := ""
	err := s.Store.Transact(ctx, func(tx Tx) error {
		for _, c := range tx.ListClaims(tenant, env) {
			if c.State == attachmentsv1.DomainActive {
				host = c.Hostname
				return nil
			}
		}
		return domain.NewError(domain.CodeNotFound, "no active route")
	})
	return host, err
}
