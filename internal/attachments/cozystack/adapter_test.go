package cozystack

import (
	"context"
	"testing"
	"time"

	"github.com/keir-research/ai-native-paas/internal/attachments/domain"
	attachmentsv1 "github.com/keir-research/ai-native-paas/pkg/contracts/attachments/v1"
)

type fakeBackend struct {
	spec                     ResourceSpec
	status                   ResourceStatus
	found                    bool
	ensureCalls, deleteCalls int
}

func (b *fakeBackend) Ensure(_ context.Context, spec ResourceSpec, _ string) (ResourceStatus, error) {
	b.spec = spec
	b.ensureCalls++
	return b.status, nil
}
func (b *fakeBackend) Find(context.Context, string) (ResourceStatus, bool, error) {
	return b.status, b.found, nil
}
func (b *fakeBackend) Get(context.Context, string) (ResourceStatus, error) { return b.status, nil }
func (b *fakeBackend) IssueCredential(context.Context, string, []string) (Credential, error) {
	return Credential{ID: "cred"}, nil
}
func (b *fakeBackend) RevokeCredential(context.Context, string, string) error { return nil }
func (b *fakeBackend) CreateBackup(context.Context, string) (string, error)   { return "backup", nil }
func (b *fakeBackend) Delete(context.Context, string) error                   { b.deleteCalls++; return nil }
func plan(t *testing.T, serviceType attachmentsv1.ServiceType) domain.ServicePlan {
	t.Helper()
	value, err := domain.NewServicePlan("small", 1, serviceType, "cozystack", "small", "mapping-v1", false, []string{"read"}, true, true, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return value
}
func instance() domain.ServiceInstance {
	return domain.ServiceInstance{ID: "instance-1", TenantID: "tenant-1", ProviderID: "provider-1", ProviderOperationKey: "operation-1"}
}
func TestCozystackAdapter_CreatePostgresWithPlanMapping(t *testing.T) {
	backend := &fakeBackend{status: ResourceStatus{ProviderID: "pg-1"}}
	_, err := (Adapter{Backend: backend}).EnsureInstance(context.Background(), plan(t, attachmentsv1.ServicePostgreSQL), instance(), map[string]string{"platform.tenant_id": "tenant-1"})
	if err != nil || backend.spec.ServiceType != "postgresql" || backend.spec.MappingVersion != "mapping-v1" {
		t.Fatalf("spec=%+v err=%v", backend.spec, err)
	}
}
func TestCozystackAdapter_CreateRedisWithTenantLabels(t *testing.T) {
	backend := &fakeBackend{}
	_, err := (Adapter{Backend: backend}).EnsureInstance(context.Background(), plan(t, attachmentsv1.ServiceRedis), instance(), map[string]string{"platform.tenant_id": "tenant-1"})
	if err != nil || backend.spec.Labels["platform.tenant_id"] != "tenant-1" {
		t.Fatalf("spec=%+v err=%v", backend.spec, err)
	}
}
func TestCozystackAdapter_ReadReadyCondition(t *testing.T) {
	backend := &fakeBackend{status: ResourceStatus{ProviderID: "pg-1", Ready: true}}
	value, err := (Adapter{Backend: backend}).GetInstance(context.Background(), plan(t, attachmentsv1.ServicePostgreSQL), instance())
	if err != nil || !value.Ready {
		t.Fatalf("value=%+v err=%v", value, err)
	}
}
func TestCozystackAdapter_FindExistingResourceByInstanceID(t *testing.T) {
	backend := &fakeBackend{status: ResourceStatus{ProviderID: "pg-1"}, found: true}
	value, found, err := (Adapter{Backend: backend}).FindInstance(context.Background(), plan(t, attachmentsv1.ServicePostgreSQL), instance())
	if err != nil || !found || value.ProviderID != "pg-1" {
		t.Fatalf("value=%+v found=%v err=%v", value, found, err)
	}
}
func TestCozystackAdapter_DeleteRequiresExplicitCall(t *testing.T) {
	backend := &fakeBackend{}
	adapter := Adapter{Backend: backend}
	_, _ = adapter.EnsureInstance(context.Background(), plan(t, attachmentsv1.ServicePostgreSQL), instance(), nil)
	if backend.deleteCalls != 0 {
		t.Fatal("implicit delete")
	}
	if err := adapter.DeleteInstance(context.Background(), plan(t, attachmentsv1.ServicePostgreSQL), instance()); err != nil || backend.deleteCalls != 1 {
		t.Fatalf("deletes=%d err=%v", backend.deleteCalls, err)
	}
}
