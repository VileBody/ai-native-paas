// Package cozystack adapts managed Cozystack resources to the Attachments port.
package cozystack

import (
	"context"
	"errors"

	"github.com/keir-research/ai-native-paas/internal/attachments/application"
	"github.com/keir-research/ai-native-paas/internal/attachments/domain"
)

var ErrUnavailable = errors.New("cozystack unavailable")

type ResourceSpec struct {
	InstanceID, TenantID, ServiceType, ProviderPlan, MappingVersion string
	Labels                                                          map[string]string
}
type ResourceStatus struct {
	ProviderID, Endpoint, FailureCode string
	Ready, FailedFinal                bool
}
type Credential struct {
	ID           string
	Values       map[string][]byte
	Capabilities []string
}

type Backend interface {
	Ensure(context.Context, ResourceSpec, string) (ResourceStatus, error)
	Find(context.Context, string) (ResourceStatus, bool, error)
	Get(context.Context, string) (ResourceStatus, error)
	IssueCredential(context.Context, string, []string) (Credential, error)
	RevokeCredential(context.Context, string, string) error
	CreateBackup(context.Context, string) (string, error)
	Delete(context.Context, string) error
}

type Adapter struct{ Backend Backend }

var _ application.ManagedServiceProvider = Adapter{}

func providerError(code string, err error) error {
	if err == nil {
		return nil
	}
	return &application.ProviderError{Code: code, Retryable: errors.Is(err, ErrUnavailable), Cause: err}
}
func spec(plan domain.ServicePlan, instance domain.ServiceInstance, labels map[string]string) ResourceSpec {
	copied := map[string]string{}
	for key, value := range labels {
		copied[key] = value
	}
	return ResourceSpec{InstanceID: instance.ID, TenantID: instance.TenantID, ServiceType: string(plan.Type), ProviderPlan: plan.ProviderPlan, MappingVersion: plan.ProviderMappingVersion, Labels: copied}
}
func result(value ResourceStatus) application.ProviderInstanceResult {
	return application.ProviderInstanceResult{ProviderID: value.ProviderID, Endpoint: value.Endpoint, FailureCode: value.FailureCode, Ready: value.Ready, FailedFinal: value.FailedFinal}
}
func (a Adapter) available() error {
	if a.Backend == nil {
		return &application.ProviderError{Code: "COZYSTACK_UNAVAILABLE", Retryable: true, Cause: ErrUnavailable}
	}
	return nil
}
func (a Adapter) EnsureInstance(ctx context.Context, plan domain.ServicePlan, instance domain.ServiceInstance, labels map[string]string) (application.ProviderInstanceResult, error) {
	if err := a.available(); err != nil {
		return application.ProviderInstanceResult{}, err
	}
	value, err := a.Backend.Ensure(ctx, spec(plan, instance, labels), instance.ProviderOperationKey)
	return result(value), providerError("COZYSTACK_ENSURE_FAILED", err)
}
func (a Adapter) FindInstance(ctx context.Context, _ domain.ServicePlan, instance domain.ServiceInstance) (application.ProviderInstanceResult, bool, error) {
	if err := a.available(); err != nil {
		return application.ProviderInstanceResult{}, false, err
	}
	value, found, err := a.Backend.Find(ctx, instance.ID)
	return result(value), found, providerError("COZYSTACK_FIND_FAILED", err)
}
func (a Adapter) GetInstance(ctx context.Context, _ domain.ServicePlan, instance domain.ServiceInstance) (application.ProviderInstanceResult, error) {
	if err := a.available(); err != nil {
		return application.ProviderInstanceResult{}, err
	}
	value, err := a.Backend.Get(ctx, instance.ProviderID)
	return result(value), providerError("COZYSTACK_GET_FAILED", err)
}
func (a Adapter) IssueCredential(ctx context.Context, _ domain.ServicePlan, instance domain.ServiceInstance, binding domain.ServiceBinding) (application.ProviderCredential, error) {
	if err := a.available(); err != nil {
		return application.ProviderCredential{}, err
	}
	value, err := a.Backend.IssueCredential(ctx, instance.ProviderID, append([]string(nil), binding.Capabilities...))
	if err != nil {
		return application.ProviderCredential{}, providerError("COZYSTACK_CREDENTIAL_FAILED", err)
	}
	return application.ProviderCredential{CredentialID: value.ID, Values: value.Values, Capabilities: value.Capabilities}, nil
}
func (a Adapter) RevokeCredential(ctx context.Context, _ domain.ServicePlan, instance domain.ServiceInstance, id string) error {
	if err := a.available(); err != nil {
		return err
	}
	return providerError("COZYSTACK_REVOKE_FAILED", a.Backend.RevokeCredential(ctx, instance.ProviderID, id))
}
func (a Adapter) CreateBackup(ctx context.Context, _ domain.ServicePlan, instance domain.ServiceInstance) (string, error) {
	if err := a.available(); err != nil {
		return "", err
	}
	id, err := a.Backend.CreateBackup(ctx, instance.ProviderID)
	return id, providerError("COZYSTACK_BACKUP_FAILED", err)
}
func (a Adapter) DeleteInstance(ctx context.Context, _ domain.ServicePlan, instance domain.ServiceInstance) error {
	if err := a.available(); err != nil {
		return err
	}
	return providerError("COZYSTACK_DELETE_FAILED", a.Backend.Delete(ctx, instance.ProviderID))
}
