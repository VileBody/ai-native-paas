package application_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/keir-research/ai-native-paas/internal/attachments/application"
	"github.com/keir-research/ai-native-paas/internal/attachments/domain"
	"github.com/keir-research/ai-native-paas/internal/attachments/memory"
	"github.com/keir-research/ai-native-paas/internal/attachments/testkit"
	attachmentsv1 "github.com/keir-research/ai-native-paas/pkg/contracts/attachments/v1"
)

const sentinel = "TDD_SECRET_DO_NOT_LEAK_5f49c1"

type fixture struct {
	service  *application.Service
	store    *memory.Store
	clock    *testkit.Clock
	vault    *testkit.Vault
	provider *testkit.Provider
	dns      *testkit.DNS
	certs    *testkit.Certificates
	runtime  *testkit.Runtime
	logger   *testkit.Logger
	approval *testkit.Approvals
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	clock := testkit.NewClock()
	store := memory.New()
	vault := testkit.NewVault()
	provider := &testkit.Provider{EnsureResult: application.ProviderInstanceResult{ProviderID: "provider-1", Endpoint: "db.local", Ready: true}, GetResult: application.ProviderInstanceResult{ProviderID: "provider-1", Endpoint: "db.local", Ready: true}}
	dns := &testkit.DNS{Values: map[string][]string{}}
	certs := &testkit.Certificates{EnsureResult: application.CertificateResult{CertificateID: "cert-1", Status: application.CertificateReady}}
	runtime := testkit.NewRuntime()
	logger := &testkit.Logger{}
	approval := &testkit.Approvals{Grants: map[string][3]string{}}
	service := &application.Service{Store: store, Environments: &testkit.Environments{Values: map[string]application.EnvironmentRef{"env-1": {TenantID: "tenant-1", ApplicationID: "app-1", EnvironmentID: "env-1", Name: "production", Ready: true}}}, Secrets: vault, Provider: provider, DNS: dns, Certificates: certs, Approvals: approval, Runtime: runtime, Commerce: &testkit.Commerce{Allowed: true}, Usage: &testkit.Usage{}, Logger: logger, Clock: clock, IDs: &application.SequentialIDs{}, DefaultDomain: "apps.example.test", DNSObservationDelay: time.Minute, DomainQuarantine: time.Hour}
	plan, err := domain.NewServicePlan("pg-small", 1, attachmentsv1.ServicePostgreSQL, "cozystack", "small", "mapping-v1", false, []string{"connect", "read", "write"}, true, true, clock.Now())
	if err != nil || service.RegisterServicePlan(context.Background(), plan, "system") != nil {
		t.Fatalf("register plan: %v", err)
	}
	return &fixture{service: service, store: store, clock: clock, vault: vault, provider: provider, dns: dns, certs: certs, runtime: runtime, logger: logger, approval: approval}
}

func (f *fixture) setRuntime(t *testing.T, name, value, key string) attachmentsv1.SecretMetadata {
	t.Helper()
	metadata, _, err := f.service.SetSecret(context.Background(), application.SetSecretRequest{TenantID: "tenant-1", ApplicationID: "app-1", EnvironmentID: "env-1", Name: name, Scope: attachmentsv1.SecretScopeRuntime, Phase: attachmentsv1.SecretPhaseRuntime, Value: []byte(value), ActorID: "user-1", IdempotencyKey: key})
	if err != nil {
		t.Fatal(err)
	}
	return metadata
}

func (f *fixture) provision(t *testing.T) domain.ServiceInstance {
	t.Helper()
	value, err := f.service.ProvisionService(context.Background(), application.ProvisionServiceRequest{TenantID: "tenant-1", Name: "primary", PlanID: "pg-small", ActorID: "user-1", IdempotencyKey: "provision-1"})
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func (f *fixture) bind(t *testing.T) (domain.ServiceBinding, attachmentsv1.AttachmentSnapshotRef) {
	t.Helper()
	instance := f.provision(t)
	binding, snapshot, err := f.service.BindService(context.Background(), application.BindServiceRequest{TenantID: "tenant-1", ApplicationID: "app-1", EnvironmentID: "env-1", InstanceID: instance.ID, Capabilities: []string{"read"}, ActorID: "user-1", IdempotencyKey: "bind-1"})
	if err != nil {
		t.Fatal(err)
	}
	return binding, snapshot
}

func containsJSON(value any, needle string) bool {
	raw, _ := json.Marshal(value)
	return strings.Contains(string(raw), needle)
}

func TestSecret_SetStoresValueOnlyThroughVaultPort(t *testing.T) {
	f := newFixture(t)
	m := f.setRuntime(t, "API_TOKEN", sentinel, "s1")
	if !f.vault.Contains(sentinel) || containsJSON(m, sentinel) {
		t.Fatal("secret boundary violated")
	}
}
func TestSecret_GetReturnsMetadataNotValue(t *testing.T) {
	f := newFixture(t)
	f.setRuntime(t, "API_TOKEN", sentinel, "s1")
	values, err := f.service.ListSecretMetadata(context.Background(), "tenant-1", "env-1")
	if err != nil || len(values) != 1 || containsJSON(values, sentinel) {
		t.Fatalf("values=%+v err=%v", values, err)
	}
}
func TestSecret_UpdateCreatesNewVersion(t *testing.T) {
	f := newFixture(t)
	first := f.setRuntime(t, "API_TOKEN", "one", "s1")
	second := f.setRuntime(t, "API_TOKEN", "two", "s2")
	if second.Version != first.Version+1 || !f.vault.Contains("two") {
		t.Fatalf("first=%+v second=%+v", first, second)
	}
}
func TestSecret_DeleteMarksMetadataDeletedAndRemovesVaultValue(t *testing.T) {
	f := newFixture(t)
	f.setRuntime(t, "API_TOKEN", sentinel, "s1")
	var id string
	_ = f.store.Transact(context.Background(), func(tx application.Tx) error {
		set, _ := tx.FindSecretSet("tenant-1", "env-1")
		secret, _ := tx.FindSecret(set.ID, "API_TOKEN", attachmentsv1.SecretScopeRuntime)
		id = secret.ID
		return nil
	})
	_, err := f.service.DeleteSecret(context.Background(), application.DeleteSecretRequest{TenantID: "tenant-1", EnvironmentID: "env-1", SecretID: id, ActorID: "user-1", IdempotencyKey: "delete-1"})
	values, listErr := f.service.ListSecretMetadata(context.Background(), "tenant-1", "env-1")
	if err != nil || listErr != nil || len(values) != 1 || !values[0].Deleted || f.vault.Contains(sentinel) {
		t.Fatalf("values=%+v err=%v list=%v", values, err, listErr)
	}
}
func TestSecret_NameValidationRejectsReservedVariables(t *testing.T) {
	f := newFixture(t)
	_, _, err := f.service.SetSecret(context.Background(), application.SetSecretRequest{TenantID: "tenant-1", ApplicationID: "app-1", EnvironmentID: "env-1", Name: "DATABASE_URL", Scope: attachmentsv1.SecretScopeRuntime, Phase: attachmentsv1.SecretPhaseRuntime, Value: []byte(sentinel), IdempotencyKey: "reserved"})
	if err == nil {
		t.Fatal("reserved variable accepted")
	}
}
func TestSecret_CrossTenantAccessDenied(t *testing.T) {
	f := newFixture(t)
	f.setRuntime(t, "API_TOKEN", sentinel, "s1")
	if _, err := f.service.ListSecretMetadata(context.Background(), "tenant-2", "env-1"); err == nil {
		t.Fatal("cross-tenant read accepted")
	}
}

func TestSecret_NotPresentInAudit(t *testing.T) {
	f := newFixture(t)
	f.setRuntime(t, "API_TOKEN", sentinel, "s1")
	if containsJSON(f.store.Audit(), sentinel) {
		t.Fatal("audit leaked secret")
	}
}
func TestSecret_NotPresentInDomainEvents(t *testing.T) {
	f := newFixture(t)
	f.setRuntime(t, "API_TOKEN", sentinel, "s1")
	if containsJSON(f.store.Outbox(), sentinel) {
		t.Fatal("outbox leaked secret")
	}
}
func TestSecret_NotPresentInPublicErrors(t *testing.T) {
	f := newFixture(t)
	_, _, err := f.service.SetSecret(context.Background(), application.SetSecretRequest{TenantID: "tenant-1", ApplicationID: "app-1", EnvironmentID: "env-1", Name: "bad", Scope: attachmentsv1.SecretScopeRuntime, Phase: attachmentsv1.SecretPhaseRuntime, Value: []byte(sentinel), IdempotencyKey: "bad"})
	if err == nil || strings.Contains(err.Error(), sentinel) {
		t.Fatalf("err=%v", err)
	}
}
func TestSecret_NotPresentInGitOpsManifest(t *testing.T) {
	f := newFixture(t)
	f.setRuntime(t, "API_TOKEN", sentinel, "s1")
	snapshot, err := f.service.Resolve(context.Background(), "tenant-1", "env-1")
	if err != nil || containsJSON(snapshot, sentinel) {
		t.Fatalf("snapshot=%+v err=%v", snapshot, err)
	}
}
func TestSecret_NotPresentInBuildMetadata(t *testing.T) {
	f := newFixture(t)
	_, _, err := f.service.SetSecret(context.Background(), application.SetSecretRequest{TenantID: "tenant-1", ApplicationID: "app-1", EnvironmentID: "env-1", Name: "NPM_TOKEN", Scope: attachmentsv1.SecretScopeBuild, Phase: attachmentsv1.SecretPhaseBuild, Value: []byte(sentinel), ExpiresAt: f.clock.Now().Add(time.Hour), IdempotencyKey: "build"})
	refs, refErr := f.service.ResolveBuildSecretRefs(context.Background(), "tenant-1", "env-1", attachmentsv1.SecretPhaseBuild)
	if err != nil || refErr != nil || containsJSON(refs, sentinel) {
		t.Fatalf("refs=%v err=%v/%v", refs, err, refErr)
	}
}
func TestSecret_NotPresentInStructuredLogs(t *testing.T) {
	f := newFixture(t)
	f.setRuntime(t, "API_TOKEN", sentinel, "s1")
	if f.logger.Contains(sentinel) {
		t.Fatal("logger leaked secret")
	}
}

func TestSecret_RuntimeSecretCannotBeRequestedByBuild(t *testing.T) {
	f := newFixture(t)
	f.setRuntime(t, "API_TOKEN", sentinel, "s1")
	refs, err := f.service.ResolveBuildSecretRefs(context.Background(), "tenant-1", "env-1", attachmentsv1.SecretPhaseBuild)
	if err != nil || len(refs) != 0 {
		t.Fatalf("refs=%v err=%v", refs, err)
	}
}
func TestSecret_BuildSecretCannotBeMountedAtRuntimeUnlessExplicitlyDuplicated(t *testing.T) {
	f := newFixture(t)
	_, _, err := f.service.SetSecret(context.Background(), application.SetSecretRequest{TenantID: "tenant-1", ApplicationID: "app-1", EnvironmentID: "env-1", Name: "NPM_TOKEN", Scope: attachmentsv1.SecretScopeBuild, Phase: attachmentsv1.SecretPhaseBuild, Value: []byte("build"), ExpiresAt: f.clock.Now().Add(time.Hour), IdempotencyKey: "build"})
	if err != nil {
		t.Fatal(err)
	}
	before, _ := f.service.Resolve(context.Background(), "tenant-1", "env-1")
	f.setRuntime(t, "NPM_TOKEN", "runtime", "runtime")
	after, _ := f.service.Resolve(context.Background(), "tenant-1", "env-1")
	if before.SecretSetRef == after.SecretSetRef {
		t.Fatal("runtime duplication did not create new secret-set version")
	}
}
func TestSecret_BuildSecretHasPhaseAndTTL(t *testing.T) {
	f := newFixture(t)
	_, _, err := f.service.SetSecret(context.Background(), application.SetSecretRequest{TenantID: "tenant-1", ApplicationID: "app-1", EnvironmentID: "env-1", Name: "NPM_TOKEN", Scope: attachmentsv1.SecretScopeBuild, Phase: attachmentsv1.SecretPhaseBuild, Value: []byte("x"), IdempotencyKey: "build"})
	if err == nil {
		t.Fatal("build secret without TTL accepted")
	}
}
func TestSecret_RuntimeRotationTriggersControlledRestart(t *testing.T) {
	f := newFixture(t)
	f.setRuntime(t, "API_TOKEN", "one", "s1")
	f.setRuntime(t, "API_TOKEN", "two", "s2")
	if f.runtime.Publishes != 2 {
		t.Fatalf("publishes=%d", f.runtime.Publishes)
	}
}

func TestServiceCatalog_RejectsUnknownPlan(t *testing.T) {
	f := newFixture(t)
	_, err := f.service.ProvisionService(context.Background(), application.ProvisionServiceRequest{TenantID: "tenant-1", Name: "x", PlanID: "missing", IdempotencyKey: "x"})
	if err == nil {
		t.Fatal("unknown plan accepted")
	}
}
func TestServiceCatalog_PlanHasImmutableProviderMappingVersion(t *testing.T) {
	f := newFixture(t)
	plan, _ := domain.NewServicePlan("pg-small", 1, attachmentsv1.ServicePostgreSQL, "cozystack", "small", "changed", false, []string{"read"}, true, true, f.clock.Now())
	if err := f.service.RegisterServicePlan(context.Background(), plan, "system"); err == nil {
		t.Fatal("mapping version changed")
	}
}
func TestServiceCatalog_SharedAndDedicatedPlansExposeDifferentCapabilities(t *testing.T) {
	f := newFixture(t)
	dedicated, _ := domain.NewServicePlan("pg-dedicated", 1, attachmentsv1.ServicePostgreSQL, "cozystack", "large", "v1", true, []string{"admin", "read", "write"}, true, true, f.clock.Now())
	if dedicated.Dedicated == false || reflect.DeepEqual(dedicated.Capabilities, []string{"connect", "read", "write"}) {
		t.Fatalf("dedicated=%+v", dedicated)
	}
}
func TestServiceCatalog_DisabledPlanCannotCreateNewInstance(t *testing.T) {
	f := newFixture(t)
	plan, _ := domain.NewServicePlan("disabled", 1, attachmentsv1.ServiceRedis, "cozystack", "small", "v1", false, []string{"read"}, false, false, f.clock.Now())
	_ = f.service.RegisterServicePlan(context.Background(), plan, "system")
	if _, err := f.service.ProvisionService(context.Background(), application.ProvisionServiceRequest{TenantID: "tenant-1", Name: "cache", PlanID: "disabled", IdempotencyKey: "x"}); err == nil {
		t.Fatal("disabled plan accepted")
	}
}

func TestService_ProvisionIsIdempotent(t *testing.T) {
	f := newFixture(t)
	one := f.provision(t)
	two, err := f.service.ProvisionService(context.Background(), application.ProvisionServiceRequest{TenantID: "tenant-1", Name: "primary", PlanID: "pg-small", ActorID: "user-1", IdempotencyKey: "provision-1"})
	if err != nil || one.ID != two.ID || f.provider.EnsureCalls != 1 {
		t.Fatalf("one=%s two=%s calls=%d err=%v", one.ID, two.ID, f.provider.EnsureCalls, err)
	}
}
func TestService_ProviderTimeoutLeavesRetryableState(t *testing.T) {
	f := newFixture(t)
	f.provider.EnsureErr = errors.New("timeout")
	value, err := f.service.ProvisionService(context.Background(), application.ProvisionServiceRequest{TenantID: "tenant-1", Name: "primary", PlanID: "pg-small", IdempotencyKey: "p"})
	if err == nil || value.State != attachmentsv1.ServiceFailedRetryable {
		t.Fatalf("value=%+v err=%v", value, err)
	}
}
func TestService_ReconcileFindsProviderResourceAfterLostResponse(t *testing.T) {
	f := newFixture(t)
	f.provider.EnsureErr = &application.ProviderError{Code: "TIMEOUT", Retryable: true}
	value, _ := f.service.ProvisionService(context.Background(), application.ProvisionServiceRequest{TenantID: "tenant-1", Name: "primary", PlanID: "pg-small", IdempotencyKey: "p"})
	f.provider.EnsureErr = nil
	f.provider.GetResult = application.ProviderInstanceResult{ProviderID: "recovered", Ready: true}
	value, err := f.service.ReconcileService(context.Background(), "tenant-1", value.ID, "system")
	if err != nil || value.State != attachmentsv1.ServiceReady || value.ProviderID != "recovered" {
		t.Fatalf("value=%+v err=%v", value, err)
	}
}
func TestService_ReadyOnlyAfterProviderConditionReady(t *testing.T) {
	f := newFixture(t)
	f.provider.EnsureResult.Ready = false
	value := f.provision(t)
	if value.State != attachmentsv1.ServiceProvisioning {
		t.Fatalf("state=%s", value.State)
	}
	f.provider.GetResult.Ready = true
	value, err := f.service.ReconcileService(context.Background(), "tenant-1", value.ID, "system")
	if err != nil || value.State != attachmentsv1.ServiceReady {
		t.Fatalf("value=%+v err=%v", value, err)
	}
}
func TestService_FailedFinalHasStableUserError(t *testing.T) {
	f := newFixture(t)
	f.provider.EnsureResult = application.ProviderInstanceResult{FailedFinal: true, FailureCode: "PLAN_UNAVAILABLE"}
	value, err := f.service.ProvisionService(context.Background(), application.ProvisionServiceRequest{TenantID: "tenant-1", Name: "primary", PlanID: "pg-small", IdempotencyKey: "p"})
	if err != nil || value.State != attachmentsv1.ServiceFailedFinal || value.FailureCode != "PLAN_UNAVAILABLE" || strings.Contains(value.FailureMessage, "provider-") {
		t.Fatalf("value=%+v err=%v", value, err)
	}
}

type labelProvider struct {
	*testkit.Provider
	labels map[string]string
}

func (p *labelProvider) EnsureInstance(ctx context.Context, plan domain.ServicePlan, instance domain.ServiceInstance, labels map[string]string) (application.ProviderInstanceResult, error) {
	p.labels = map[string]string{}
	for key, value := range labels {
		p.labels[key] = value
	}
	return p.Provider.EnsureInstance(ctx, plan, instance, labels)
}
func TestService_ProviderResourceTaggedWithTenantAndInstanceIDs(t *testing.T) {
	f := newFixture(t)
	recorder := &labelProvider{Provider: f.provider}
	f.service.Provider = recorder
	value := f.provision(t)
	if recorder.labels["platform.tenant_id"] != "tenant-1" || recorder.labels["platform.instance_id"] != value.ID {
		t.Fatalf("labels=%v", recorder.labels)
	}
}

func TestBinding_RequiresReadyServiceAndValidEnvironment(t *testing.T) {
	f := newFixture(t)
	f.provider.EnsureResult.Ready = false
	value := f.provision(t)
	_, _, err := f.service.BindService(context.Background(), application.BindServiceRequest{TenantID: "tenant-1", ApplicationID: "app-1", EnvironmentID: "env-1", InstanceID: value.ID, Capabilities: []string{"read"}, IdempotencyKey: "b"})
	if err == nil {
		t.Fatal("unready service bound")
	}
}
func TestBinding_CreatesLeastPrivilegeCredential(t *testing.T) {
	f := newFixture(t)
	f.provider.Credential = application.ProviderCredential{CredentialID: "c", Values: map[string][]byte{"DATABASE_URL": []byte("x")}, Capabilities: []string{"read"}}
	binding, _ := f.bind(t)
	if !reflect.DeepEqual(binding.Capabilities, []string{"read"}) {
		t.Fatalf("caps=%v", binding.Capabilities)
	}
}
func TestBinding_StoresCredentialInOpenBao(t *testing.T) {
	f := newFixture(t)
	f.provider.Credential = application.ProviderCredential{CredentialID: "c", Values: map[string][]byte{"DATABASE_URL": []byte(sentinel)}, Capabilities: []string{"read"}}
	f.bind(t)
	if !f.vault.Contains(sentinel) {
		t.Fatal("credential not stored")
	}
}
func TestBinding_AttachmentSnapshotContainsReferenceNotValue(t *testing.T) {
	f := newFixture(t)
	f.provider.Credential = application.ProviderCredential{CredentialID: "c", Values: map[string][]byte{"DATABASE_URL": []byte(sentinel)}, Capabilities: []string{"read"}}
	_, ref := f.bind(t)
	snapshot, _ := f.service.Resolve(context.Background(), "tenant-1", "env-1")
	if ref.SnapshotID == "" || len(snapshot.ServiceBindings) != 1 || containsJSON(snapshot, sentinel) {
		t.Fatalf("snapshot=%+v", snapshot)
	}
}
func TestBinding_RotationReplacesCredentialAtomically(t *testing.T) {
	f := newFixture(t)
	binding, before := f.bind(t)
	rotated, after, err := f.service.RotateBinding(context.Background(), application.RotateBindingRequest{TenantID: "tenant-1", BindingID: binding.ID, ActorID: "user-1", IdempotencyKey: "rotate"})
	if err != nil || rotated.State != attachmentsv1.BindingActive || after.Version <= before.Version || f.provider.RevokeCalls != 1 {
		t.Fatalf("rotated=%+v after=%+v err=%v", rotated, after, err)
	}
}
func TestBinding_RevokeRemovesRuntimeAccess(t *testing.T) {
	f := newFixture(t)
	binding, _ := f.bind(t)
	revoked, _, err := f.service.RevokeBinding(context.Background(), application.RevokeBindingRequest{TenantID: "tenant-1", BindingID: binding.ID, ActorID: "user-1", IdempotencyKey: "revoke"})
	snapshot, _ := f.service.Resolve(context.Background(), "tenant-1", "env-1")
	if err != nil || revoked.State != attachmentsv1.BindingRevoked || len(snapshot.ServiceBindings) != 0 {
		t.Fatalf("revoked=%+v snapshot=%+v err=%v", revoked, snapshot, err)
	}
}
func TestBinding_DeleteApplicationRevokesBindingButRetainsService(t *testing.T) {
	f := newFixture(t)
	binding, _ := f.bind(t)
	if err := f.service.DeleteApplicationAttachments(context.Background(), "tenant-1", "app-1", "user-1"); err != nil {
		t.Fatal(err)
	}
	binding, _ = f.service.GetBinding(context.Background(), "tenant-1", binding.ID)
	if binding.State != attachmentsv1.BindingRevoked || f.provider.DeleteCalls != 0 {
		t.Fatalf("binding=%+v deletes=%d", binding, f.provider.DeleteCalls)
	}
}

func TestServicePurge_RequiresValidApprovalReference(t *testing.T) {
	f := newFixture(t)
	value := f.provision(t)
	if _, err := f.service.PurgeService(context.Background(), application.PurgeServiceRequest{TenantID: "tenant-1", InstanceID: value.ID, ActorID: "user-1"}); err == nil {
		t.Fatal("purge without approval accepted")
	}
}
func TestServicePurge_ApprovalMustMatchInstanceAndActor(t *testing.T) {
	f := newFixture(t)
	value := f.provision(t)
	f.approval.Grants["grant"] = [3]string{"tenant-1", "other", value.ID}
	if _, err := f.service.PurgeService(context.Background(), application.PurgeServiceRequest{TenantID: "tenant-1", InstanceID: value.ID, ActorID: "user-1", ApprovalRef: "grant"}); err == nil {
		t.Fatal("mismatched approval accepted")
	}
}
func TestServicePurge_CreatesFinalBackupWhenPolicyRequires(t *testing.T) {
	f := newFixture(t)
	value := f.provision(t)
	f.approval.Grants["grant"] = [3]string{"tenant-1", "user-1", value.ID}
	value, err := f.service.PurgeService(context.Background(), application.PurgeServiceRequest{TenantID: "tenant-1", InstanceID: value.ID, ActorID: "user-1", ApprovalRef: "grant"})
	if err != nil || value.State != attachmentsv1.ServiceDeleted || value.FinalBackupID == "" {
		t.Fatalf("value=%+v err=%v", value, err)
	}
}
func TestServicePurge_IsIrreversibleAfterProviderDelete(t *testing.T) {
	f := newFixture(t)
	value := f.provision(t)
	f.approval.Grants["grant"] = [3]string{"tenant-1", "user-1", value.ID}
	_, _ = f.service.PurgeService(context.Background(), application.PurgeServiceRequest{TenantID: "tenant-1", InstanceID: value.ID, ActorID: "user-1", ApprovalRef: "grant"})
	_, err := f.service.PurgeService(context.Background(), application.PurgeServiceRequest{TenantID: "tenant-1", InstanceID: value.ID, ActorID: "user-1", ApprovalRef: "other"})
	if err != nil || f.provider.DeleteCalls != 1 {
		t.Fatalf("deletes=%d err=%v", f.provider.DeleteCalls, err)
	}
}
func TestServicePurge_AuditContainsNoCredential(t *testing.T) {
	f := newFixture(t)
	value := f.provision(t)
	f.approval.Grants["grant"] = [3]string{"tenant-1", "user-1", value.ID}
	_, _ = f.service.PurgeService(context.Background(), application.PurgeServiceRequest{TenantID: "tenant-1", InstanceID: value.ID, ActorID: "user-1", ApprovalRef: "grant"})
	if containsJSON(f.store.Audit(), sentinel) || containsJSON(f.store.Audit(), "postgres://secret") {
		t.Fatal("audit leaked credential")
	}
}

func TestGeneratedDomain_IsDeterministicAndTenantUnique(t *testing.T) {
	one := newFixture(t)
	two := newFixture(t)
	a, _ := one.service.CreateGeneratedDomain(context.Background(), application.GeneratedDomainRequest{TenantID: "tenant-1", ApplicationID: "app-1", EnvironmentID: "env-1", PreferredName: "booking", IdempotencyKey: "d1"})
	b, _ := two.service.CreateGeneratedDomain(context.Background(), application.GeneratedDomainRequest{TenantID: "tenant-1", ApplicationID: "app-1", EnvironmentID: "env-1", PreferredName: "booking", IdempotencyKey: "d2"})
	if a.Hostname != b.Hostname || !strings.HasSuffix(a.Hostname, ".apps.example.test") {
		t.Fatalf("a=%s b=%s", a.Hostname, b.Hostname)
	}
}
func TestGeneratedDomain_ReservedNamesRejected(t *testing.T) {
	f := newFixture(t)
	if _, err := f.service.CreateGeneratedDomain(context.Background(), application.GeneratedDomainRequest{TenantID: "tenant-1", ApplicationID: "app-1", EnvironmentID: "env-1", PreferredName: "admin", IdempotencyKey: "d"}); err == nil {
		t.Fatal("reserved domain accepted")
	}
}
func TestGeneratedDomain_RoutePointsToCorrectEnvironment(t *testing.T) {
	f := newFixture(t)
	claim, err := f.service.CreateGeneratedDomain(context.Background(), application.GeneratedDomainRequest{TenantID: "tenant-1", ApplicationID: "app-1", EnvironmentID: "env-1", PreferredName: "booking", IdempotencyKey: "d"})
	route, routeErr := f.service.RouteForEnvironment(context.Background(), "tenant-1", "env-1")
	if err != nil || routeErr != nil || route != claim.Hostname {
		t.Fatalf("claim=%+v route=%s err=%v/%v", claim, route, err, routeErr)
	}
}

func claimDomain(t *testing.T, f *fixture) domain.DomainClaim {
	t.Helper()
	value, err := f.service.ClaimCustomDomain(context.Background(), application.ClaimDomainRequest{TenantID: "tenant-1", ApplicationID: "app-1", EnvironmentID: "env-1", Hostname: "booking.example.com", ActorID: "user-1", IdempotencyKey: "domain"})
	if err != nil {
		t.Fatal(err)
	}
	return value
}
func verifyDomain(t *testing.T, f *fixture, claim domain.DomainClaim) domain.DomainClaim {
	t.Helper()
	f.dns.Values[claim.ChallengeName] = []string{claim.ChallengeValue}
	if _, err := f.service.VerifyDomain(context.Background(), application.VerifyDomainRequest{TenantID: "tenant-1", ClaimID: claim.ID, ActorID: "user-1"}); err != nil {
		t.Fatal(err)
	}
	f.clock.Advance(time.Minute)
	value, err := f.service.VerifyDomain(context.Background(), application.VerifyDomainRequest{TenantID: "tenant-1", ClaimID: claim.ID, ActorID: "user-1"})
	if err != nil {
		t.Fatal(err)
	}
	return value
}
func TestDomainClaim_DuplicateActiveClaimRejected(t *testing.T) {
	f := newFixture(t)
	_ = claimDomain(t, f)
	if _, err := f.service.ClaimCustomDomain(context.Background(), application.ClaimDomainRequest{TenantID: "tenant-1", ApplicationID: "app-1", EnvironmentID: "env-1", Hostname: "BOOKING.example.com", IdempotencyKey: "other"}); err == nil {
		t.Fatal("duplicate claim accepted")
	}
}
func TestDomainClaim_ReturnsTXTChallenge(t *testing.T) {
	claim := claimDomain(t, newFixture(t))
	if !strings.HasPrefix(claim.ChallengeName, "_paas-verification.") || claim.ChallengeValue == "" {
		t.Fatalf("claim=%+v", claim)
	}
}
func TestDomainClaim_WrongTXTDoesNotVerify(t *testing.T) {
	f := newFixture(t)
	claim := claimDomain(t, f)
	f.dns.Values[claim.ChallengeName] = []string{"wrong"}
	if _, err := f.service.VerifyDomain(context.Background(), application.VerifyDomainRequest{TenantID: "tenant-1", ClaimID: claim.ID}); err == nil {
		t.Fatal("wrong TXT accepted")
	}
}
func TestDomainClaim_CorrectTXTVerifiesOwnership(t *testing.T) {
	f := newFixture(t)
	value := verifyDomain(t, f, claimDomain(t, f))
	if value.State != attachmentsv1.DomainActive {
		t.Fatalf("state=%s", value.State)
	}
}
func TestDomainClaim_DNSRebindingDuringVerificationIsRejected(t *testing.T) {
	f := newFixture(t)
	claim := claimDomain(t, f)
	f.dns.Values[claim.ChallengeName] = []string{claim.ChallengeValue}
	_, _ = f.service.VerifyDomain(context.Background(), application.VerifyDomainRequest{TenantID: "tenant-1", ClaimID: claim.ID})
	f.clock.Advance(time.Minute)
	f.dns.Values[claim.ChallengeName] = []string{claim.ChallengeValue, "changed"}
	value, err := f.service.VerifyDomain(context.Background(), application.VerifyDomainRequest{TenantID: "tenant-1", ClaimID: claim.ID})
	if err == nil || value.State != attachmentsv1.DomainVerificationFailed {
		t.Fatalf("value=%+v err=%v", value, err)
	}
}
func TestDomainClaim_RouteNotCreatedBeforeVerification(t *testing.T) {
	f := newFixture(t)
	_ = claimDomain(t, f)
	if _, err := f.service.RouteForEnvironment(context.Background(), "tenant-1", "env-1"); err == nil {
		t.Fatal("route active before verification")
	}
}
func TestDomainClaim_TLSPendingDoesNotReportActive(t *testing.T) {
	f := newFixture(t)
	f.certs.EnsureResult.Status = application.CertificatePending
	value := verifyDomain(t, f, claimDomain(t, f))
	if value.State != attachmentsv1.DomainTLSPending {
		t.Fatalf("state=%s", value.State)
	}
}
func TestDomainClaim_CertificateReadyActivatesClaim(t *testing.T) {
	f := newFixture(t)
	value := verifyDomain(t, f, claimDomain(t, f))
	if value.State != attachmentsv1.DomainActive {
		t.Fatalf("state=%s", value.State)
	}
}
func TestDomainClaim_DeleteEntersQuarantine(t *testing.T) {
	f := newFixture(t)
	claim := verifyDomain(t, f, claimDomain(t, f))
	value, err := f.service.DeleteDomain(context.Background(), application.DeleteDomainRequest{TenantID: "tenant-1", ClaimID: claim.ID, ActorID: "user-1"})
	if err != nil || value.State != attachmentsv1.DomainQuarantined || value.QuarantineUntil.IsZero() {
		t.Fatalf("value=%+v err=%v", value, err)
	}
}
func TestDomainClaim_QuarantinePreventsImmediateTakeover(t *testing.T) {
	f := newFixture(t)
	claim := verifyDomain(t, f, claimDomain(t, f))
	_, _ = f.service.DeleteDomain(context.Background(), application.DeleteDomainRequest{TenantID: "tenant-1", ClaimID: claim.ID, ActorID: "user-1"})
	if _, err := f.service.ClaimCustomDomain(context.Background(), application.ClaimDomainRequest{TenantID: "tenant-1", ApplicationID: "app-1", EnvironmentID: "env-1", Hostname: claim.Hostname, IdempotencyKey: "takeover"}); err == nil {
		t.Fatal("quarantined domain taken over")
	}
}

func TestAttachmentSnapshot_IsImmutable(t *testing.T) {
	f := newFixture(t)
	f.setRuntime(t, "API_TOKEN", "one", "s1")
	snapshot, _ := f.service.Resolve(context.Background(), "tenant-1", "env-1")
	err := f.store.Transact(context.Background(), func(tx application.Tx) error {
		return tx.PutSnapshot(domain.AttachmentSnapshot{Value: snapshot, ContentHash: domain.Hash("changed")})
	})
	if err == nil {
		t.Fatal("snapshot mutation accepted")
	}
}
func TestAttachmentSnapshot_ChangesVersionWhenBindingChanges(t *testing.T) {
	f := newFixture(t)
	f.setRuntime(t, "API_TOKEN", "one", "s1")
	before, _ := f.service.Resolve(context.Background(), "tenant-1", "env-1")
	_, after := f.bind(t)
	if after.Version <= before.Version {
		t.Fatalf("before=%d after=%d", before.Version, after.Version)
	}
}
func TestAttachmentSnapshot_SecretRotationChangesVersionWithoutValueDisclosure(t *testing.T) {
	f := newFixture(t)
	f.setRuntime(t, "API_TOKEN", "one", "s1")
	before, _ := f.service.Resolve(context.Background(), "tenant-1", "env-1")
	f.setRuntime(t, "API_TOKEN", sentinel, "s2")
	after, _ := f.service.Resolve(context.Background(), "tenant-1", "env-1")
	if after.Version <= before.Version || containsJSON(after, sentinel) {
		t.Fatalf("before=%+v after=%+v", before, after)
	}
}
func TestAttachmentSnapshot_RuntimeUpdateIsIdempotent(t *testing.T) {
	f := newFixture(t)
	f.setRuntime(t, "API_TOKEN", "one", "same")
	f.setRuntime(t, "API_TOKEN", "one", "same")
	if f.runtime.Publishes != 1 {
		t.Fatalf("publishes=%d", f.runtime.Publishes)
	}
}
func TestAttachmentSnapshot_DoesNotIncludeDeletedBinding(t *testing.T) {
	f := newFixture(t)
	binding, _ := f.bind(t)
	_, _, _ = f.service.RevokeBinding(context.Background(), application.RevokeBindingRequest{TenantID: "tenant-1", BindingID: binding.ID, ActorID: "user-1", IdempotencyKey: "revoke"})
	snapshot, _ := f.service.Resolve(context.Background(), "tenant-1", "env-1")
	if len(snapshot.ServiceBindings) != 0 {
		t.Fatalf("bindings=%v", snapshot.ServiceBindings)
	}
}
