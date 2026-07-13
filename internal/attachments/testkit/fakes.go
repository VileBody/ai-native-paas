package testkit

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/keir-research/ai-native-paas/internal/attachments/application"
	"github.com/keir-research/ai-native-paas/internal/attachments/domain"
	attachmentsv1 "github.com/keir-research/ai-native-paas/pkg/contracts/attachments/v1"
	commercev1 "github.com/keir-research/ai-native-paas/pkg/contracts/commerce/v1"
)

type Clock struct {
	mu    sync.Mutex
	value time.Time
}

func NewClock() *Clock                   { return &Clock{value: time.Date(2026, 7, 13, 10, 0, 0, 0, time.UTC)} }
func (c *Clock) Now() time.Time          { c.mu.Lock(); defer c.mu.Unlock(); return c.value }
func (c *Clock) Advance(d time.Duration) { c.mu.Lock(); c.value = c.value.Add(d); c.mu.Unlock() }

type Environments struct {
	Values map[string]application.EnvironmentRef
	Err    error
}

func (e *Environments) ResolveEnvironment(_ context.Context, tenant, id string) (application.EnvironmentRef, error) {
	if e.Err != nil {
		return application.EnvironmentRef{}, e.Err
	}
	value, ok := e.Values[id]
	if !ok {
		return application.EnvironmentRef{}, domain.NewError(domain.CodeNotFound, "environment not found")
	}
	if value.TenantID != tenant {
		return application.EnvironmentRef{}, domain.NewError(domain.CodeForbidden, "environment belongs to another tenant")
	}
	return value, nil
}

type vaultEntry struct {
	value   []byte
	version string
	expires time.Time
}
type Vault struct {
	mu       sync.Mutex
	values   map[string]vaultEntry
	sequence int
}

func NewVault() *Vault { return &Vault{values: map[string]vaultEntry{}} }
func vaultKey(tenant, path, name string) string {
	return tenant + "/" + strings.Trim(path, "/") + "/" + name
}
func (v *Vault) Write(_ context.Context, tenant, path, name string, value []byte, expires time.Time) (application.SecretWriteResult, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.sequence++
	ref := strings.Trim(path, "/") + "/" + name
	version := fmt.Sprintf("v%d", v.sequence)
	v.values[vaultKey(tenant, path, name)] = vaultEntry{value: append([]byte(nil), value...), version: version, expires: expires}
	return application.SecretWriteResult{Ref: ref, Version: version}, nil
}
func (v *Vault) Metadata(_ context.Context, tenant, path, name string) (application.SecretProviderMetadata, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	entry, ok := v.values[vaultKey(tenant, path, name)]
	if !ok {
		return application.SecretProviderMetadata{Exists: false}, nil
	}
	return application.SecretProviderMetadata{Ref: strings.Trim(path, "/") + "/" + name, Version: entry.version, ExpiresAt: entry.expires, Exists: true}, nil
}
func (v *Vault) Delete(_ context.Context, tenant, path, name string) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	prefix := vaultKey(tenant, path, "")
	if name == "*" {
		for key := range v.values {
			if strings.HasPrefix(key, prefix) {
				delete(v.values, key)
			}
		}
		return nil
	}
	delete(v.values, vaultKey(tenant, path, name))
	return nil
}
func (v *Vault) Contains(value string) bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	for _, entry := range v.values {
		if string(entry.value) == value {
			return true
		}
	}
	return false
}

type Provider struct {
	mu                                                                         sync.Mutex
	EnsureResult, GetResult                                                    application.ProviderInstanceResult
	Credential                                                                 application.ProviderCredential
	EnsureErr, FindErr, GetErr, CredentialErr, RevokeErr, BackupErr, DeleteErr error
	EnsureCalls, RevokeCalls, DeleteCalls                                      int
	BackupID                                                                   string
}

type Commerce struct {
	mu      sync.Mutex
	Allowed bool
	Err     error
	Calls   int
	Last    commercev1.EntitlementRequest
}

func (c *Commerce) Check(_ context.Context, request commercev1.EntitlementRequest) (commercev1.EntitlementDecision, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.Calls++
	c.Last = request
	if c.Err != nil {
		return commercev1.EntitlementDecision{}, c.Err
	}
	return commercev1.EntitlementDecision{Allowed: c.Allowed, PolicyVersion: "test-policy-v1", PlanVersionID: "test-plan-v1"}, nil
}

type Usage struct {
	mu     sync.Mutex
	Err    error
	Events []commercev1.UsageEvent
}

func (u *Usage) Append(_ context.Context, event commercev1.UsageEvent) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.Err != nil {
		return u.Err
	}
	event.Metadata = cloneStrings(event.Metadata)
	u.Events = append(u.Events, event)
	return nil
}

func (u *Usage) Count() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return len(u.Events)
}

func cloneStrings(values map[string]string) map[string]string {
	out := make(map[string]string, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}

func (p *Provider) EnsureInstance(_ context.Context, _ domain.ServicePlan, instance domain.ServiceInstance, _ map[string]string) (application.ProviderInstanceResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.EnsureCalls++
	out := p.EnsureResult
	if out.ProviderID == "" {
		out.ProviderID = "provider-" + instance.ID
	}
	return out, p.EnsureErr
}
func (p *Provider) FindInstance(_ context.Context, _ domain.ServicePlan, instance domain.ServiceInstance) (application.ProviderInstanceResult, bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.FindErr != nil {
		return application.ProviderInstanceResult{}, false, p.FindErr
	}
	out := p.GetResult
	if out.ProviderID == "" {
		out = p.EnsureResult
	}
	return out, out.ProviderID != "" || out.Ready, nil
}
func (p *Provider) GetInstance(_ context.Context, _ domain.ServicePlan, _ domain.ServiceInstance) (application.ProviderInstanceResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := p.GetResult
	if out.ProviderID == "" {
		out = p.EnsureResult
	}
	return out, p.GetErr
}
func (p *Provider) IssueCredential(_ context.Context, _ domain.ServicePlan, _ domain.ServiceInstance, binding domain.ServiceBinding) (application.ProviderCredential, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.CredentialErr != nil {
		return application.ProviderCredential{}, p.CredentialErr
	}
	out := p.Credential
	if out.CredentialID == "" {
		out = application.ProviderCredential{CredentialID: fmt.Sprintf("credential-%s-%d", binding.ID, p.RevokeCalls+1), Values: map[string][]byte{"DATABASE_URL": []byte("postgres://secret")}, Capabilities: append([]string(nil), binding.Capabilities...)}
	}
	copied := map[string][]byte{}
	for key, value := range out.Values {
		copied[key] = append([]byte(nil), value...)
	}
	out.Values = copied
	return out, nil
}
func (p *Provider) RevokeCredential(_ context.Context, _ domain.ServicePlan, _ domain.ServiceInstance, _ string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.RevokeCalls++
	return p.RevokeErr
}
func (p *Provider) CreateBackup(_ context.Context, _ domain.ServicePlan, _ domain.ServiceInstance) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.BackupID == "" {
		p.BackupID = "backup-1"
	}
	return p.BackupID, p.BackupErr
}
func (p *Provider) DeleteInstance(_ context.Context, _ domain.ServicePlan, _ domain.ServiceInstance) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.DeleteCalls++
	return p.DeleteErr
}

type DNS struct {
	Values map[string][]string
	Err    error
}

func (d *DNS) ReadTXT(_ context.Context, name string) ([]string, error) {
	if d.Err != nil {
		return nil, d.Err
	}
	return append([]string(nil), d.Values[name]...), nil
}

type Certificates struct {
	EnsureResult, StatusResult application.CertificateResult
	EnsureErr, StatusErr       error
}

func (c *Certificates) Ensure(context.Context, string, string) (application.CertificateResult, error) {
	return c.EnsureResult, c.EnsureErr
}
func (c *Certificates) Status(context.Context, string) (application.CertificateResult, error) {
	if c.StatusResult.CertificateID == "" {
		return c.EnsureResult, c.StatusErr
	}
	return c.StatusResult, c.StatusErr
}

type Approvals struct{ Grants map[string][3]string }

func (a *Approvals) Verify(_ context.Context, ref, tenant, actor, resource string) error {
	value, ok := a.Grants[ref]
	if !ok || value != [3]string{tenant, actor, resource} {
		return errors.New("approval denied")
	}
	delete(a.Grants, ref)
	return nil
}

type Runtime struct {
	mu        sync.Mutex
	Publishes int
	Snapshots []attachmentsv1.AttachmentSnapshot
	Err       error
}

func NewRuntime() *Runtime { return &Runtime{} }
func (r *Runtime) Publish(_ context.Context, snapshot attachmentsv1.AttachmentSnapshot) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.Err != nil {
		return r.Err
	}
	r.Publishes++
	r.Snapshots = append(r.Snapshots, snapshot)
	return nil
}

type Logger struct {
	mu      sync.Mutex
	entries []string
}

func (l *Logger) Log(_ context.Context, event string, fields map[string]string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.entries = append(l.entries, event+fmt.Sprint(fields))
}
func (l *Logger) Contains(value string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, entry := range l.entries {
		if strings.Contains(entry, value) {
			return true
		}
	}
	return false
}
