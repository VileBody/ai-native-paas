// Package devadapter contains local-only provider adapters for the smoke API.
package devadapter

import (
	"context"
	"fmt"
	"log"
	"sync"

	"github.com/keir-research/ai-native-paas/internal/attachments/application"
	"github.com/keir-research/ai-native-paas/internal/attachments/domain"
	attachmentsv1 "github.com/keir-research/ai-native-paas/pkg/contracts/attachments/v1"
	commercev1 "github.com/keir-research/ai-native-paas/pkg/contracts/commerce/v1"
)

type Environments struct {
	values map[string]application.EnvironmentRef
}

func NewEnvironments(values ...application.EnvironmentRef) *Environments {
	out := &Environments{values: map[string]application.EnvironmentRef{}}
	for _, value := range values {
		out.values[value.EnvironmentID] = value
	}
	return out
}
func (e *Environments) ResolveEnvironment(_ context.Context, tenant, id string) (application.EnvironmentRef, error) {
	value, ok := e.values[id]
	if !ok {
		return application.EnvironmentRef{}, domain.NewError(domain.CodeNotFound, "environment not found")
	}
	if value.TenantID != tenant {
		return application.EnvironmentRef{}, domain.NewError(domain.CodeForbidden, "environment belongs to another tenant")
	}
	return value, nil
}

type ManagedProvider struct {
	mu          sync.Mutex
	instances   map[string]application.ProviderInstanceResult
	credentials int
}

func NewManagedProvider() *ManagedProvider {
	return &ManagedProvider{instances: map[string]application.ProviderInstanceResult{}}
}
func (p *ManagedProvider) EnsureInstance(_ context.Context, _ domain.ServicePlan, instance domain.ServiceInstance, _ map[string]string) (application.ProviderInstanceResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if value, ok := p.instances[instance.ID]; ok {
		return value, nil
	}
	value := application.ProviderInstanceResult{ProviderID: "dev-" + instance.ID, Endpoint: instance.Name + ".service.local", Ready: true}
	p.instances[instance.ID] = value
	return value, nil
}
func (p *ManagedProvider) FindInstance(_ context.Context, _ domain.ServicePlan, instance domain.ServiceInstance) (application.ProviderInstanceResult, bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	value, ok := p.instances[instance.ID]
	return value, ok, nil
}
func (p *ManagedProvider) GetInstance(_ context.Context, _ domain.ServicePlan, instance domain.ServiceInstance) (application.ProviderInstanceResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	value, ok := p.instances[instance.ID]
	if !ok {
		return application.ProviderInstanceResult{}, domain.NewError(domain.CodeNotFound, "provider instance not found")
	}
	return value, nil
}
func (p *ManagedProvider) IssueCredential(_ context.Context, _ domain.ServicePlan, instance domain.ServiceInstance, binding domain.ServiceBinding) (application.ProviderCredential, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.credentials++
	id := fmt.Sprintf("dev-credential-%d", p.credentials)
	return application.ProviderCredential{CredentialID: id, Values: map[string][]byte{"DATABASE_URL": []byte("postgres://dev-user:dev-secret@" + instance.ProviderEndpoint + "/app")}, Capabilities: append([]string(nil), binding.Capabilities...)}, nil
}
func (p *ManagedProvider) RevokeCredential(context.Context, domain.ServicePlan, domain.ServiceInstance, string) error {
	return nil
}
func (p *ManagedProvider) CreateBackup(_ context.Context, _ domain.ServicePlan, instance domain.ServiceInstance) (string, error) {
	return "dev-backup-" + instance.ID, nil
}
func (p *ManagedProvider) DeleteInstance(_ context.Context, _ domain.ServicePlan, instance domain.ServiceInstance) error {
	p.mu.Lock()
	delete(p.instances, instance.ID)
	p.mu.Unlock()
	return nil
}

type DNS struct {
	mu     sync.Mutex
	values map[string][]string
}

func NewDNS() *DNS { return &DNS{values: map[string][]string{}} }
func (d *DNS) ReadTXT(_ context.Context, name string) ([]string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.values[name]...), nil
}

type Certificates struct{}

func (Certificates) Ensure(_ context.Context, tenant, hostname string) (application.CertificateResult, error) {
	return application.CertificateResult{CertificateID: "dev-cert-" + domain.Hash(struct{ T, H string }{tenant, hostname})[:12], Status: application.CertificateReady}, nil
}
func (Certificates) Status(_ context.Context, id string) (application.CertificateResult, error) {
	return application.CertificateResult{CertificateID: id, Status: application.CertificateReady}, nil
}

type Approvals struct{}

func (Approvals) Verify(context.Context, string, string, string, string) error { return nil }

type Runtime struct {
	mu        sync.Mutex
	snapshots map[string]attachmentsv1.AttachmentSnapshot
}

func NewRuntime() *Runtime { return &Runtime{snapshots: map[string]attachmentsv1.AttachmentSnapshot{}} }
func (r *Runtime) Publish(_ context.Context, snapshot attachmentsv1.AttachmentSnapshot) error {
	r.mu.Lock()
	r.snapshots[snapshot.EnvironmentID] = snapshot
	r.mu.Unlock()
	return nil
}

type Logger struct{}

func (Logger) Log(_ context.Context, event string, fields map[string]string) {
	log.Printf("attachments event=%s fields=%v", event, fields)
}

type Commerce struct{}

func (Commerce) Check(_ context.Context, _ commercev1.EntitlementRequest) (commercev1.EntitlementDecision, error) {
	return commercev1.EntitlementDecision{Allowed: true, PolicyVersion: "dev-policy-v1", PlanVersionID: "dev-plan-v1"}, nil
}

type Usage struct{}

func (Usage) Append(_ context.Context, event commercev1.UsageEvent) error {
	log.Printf("attachments usage tenant=%s resource=%s meter=%s quantity=%d", event.TenantID, event.ResourceID, event.Meter, event.Quantity)
	return nil
}
