// Package facade adapts the rich Attachments application service to the
// deliberately small, frozen agent-facing v1 contract.
package facade

import (
	"context"
	"strings"

	"github.com/keir-research/ai-native-paas/internal/attachments/application"
	"github.com/keir-research/ai-native-paas/internal/attachments/domain"
	attachmentsv1 "github.com/keir-research/ai-native-paas/pkg/contracts/attachments/v1"
)

type Service struct {
	Core *application.Service
}

type PurgeRequest struct {
	TenantID, ServiceInstanceID, PlanHash, ActorID, ApprovalRef, IdempotencyKey string
}

var _ attachmentsv1.Service = (*Service)(nil)

func (f *Service) core() (*application.Service, error) {
	if f == nil || f.Core == nil {
		return nil, domain.NewError(domain.CodeUnavailable, "attachments service unavailable")
	}
	return f.Core, nil
}

func (f *Service) SetSecret(ctx context.Context, r attachmentsv1.SetSecretRequest) (attachmentsv1.SecretMetadata, error) {
	core, err := f.core()
	if err != nil {
		return attachmentsv1.SecretMetadata{}, err
	}
	value := []byte(r.Value)
	defer func() {
		for i := range value {
			value[i] = 0
		}
	}()
	metadata, _, err := core.SetSecret(ctx, application.SetSecretRequest{
		TenantID: r.TenantID, ApplicationID: r.ApplicationID, EnvironmentID: r.EnvironmentID,
		Name: r.Name, Scope: attachmentsv1.SecretScopeRuntime, Phase: attachmentsv1.SecretPhaseRuntime,
		Value: value, ActorID: r.ActorID, IdempotencyKey: r.IdempotencyKey,
	})
	return metadata, err
}

func (f *Service) ListSecretMetadata(ctx context.Context, tenantID, applicationID, environmentID string) ([]attachmentsv1.SecretMetadata, error) {
	core, err := f.core()
	if err != nil {
		return nil, err
	}
	if core.Environments == nil {
		return nil, domain.NewError(domain.CodeUnavailable, "environment directory unavailable")
	}
	env, err := core.Environments.ResolveEnvironment(ctx, tenantID, environmentID)
	if err != nil {
		return nil, err
	}
	if env.TenantID != tenantID || env.ApplicationID != applicationID {
		return nil, domain.NewError(domain.CodeForbidden, "application or tenant mismatch")
	}
	return core.ListSecretMetadata(ctx, tenantID, environmentID)
}

func (f *Service) Provision(ctx context.Context, r attachmentsv1.ServiceRequest) (attachmentsv1.ServiceInstanceRef, error) {
	core, err := f.core()
	if err != nil {
		return attachmentsv1.ServiceInstanceRef{}, err
	}
	planID := strings.TrimSpace(r.Plan)
	if planID == "" {
		return attachmentsv1.ServiceInstanceRef{}, domain.NewError(domain.CodeInvalidArgument, "service plan required")
	}
	if strings.TrimSpace(r.ServiceType) != "" {
		var planType attachmentsv1.ServiceType
		err = core.Store.Transact(ctx, func(tx application.Tx) error {
			plan, ok := tx.LatestPlan(planID)
			if !ok {
				return domain.NewError(domain.CodeNotFound, "service plan not found")
			}
			planType = plan.Type
			return nil
		})
		if err != nil {
			return attachmentsv1.ServiceInstanceRef{}, err
		}
		if string(planType) != strings.ToLower(strings.TrimSpace(r.ServiceType)) {
			return attachmentsv1.ServiceInstanceRef{}, domain.NewError(domain.CodeInvalidArgument, "service type does not match plan")
		}
	}
	instance, err := core.ProvisionService(ctx, application.ProvisionServiceRequest{
		TenantID: r.TenantID, Name: r.Name, PlanID: planID,
		ActorID: r.ActorID, IdempotencyKey: r.IdempotencyKey,
	})
	if err != nil {
		return attachmentsv1.ServiceInstanceRef{}, err
	}
	return attachmentsv1.ServiceInstanceRef{
		ServiceInstanceID: instance.ID, InstanceID: instance.ID, TenantID: instance.TenantID,
		PlanID: instance.PlanID, Type: instance.Type, State: string(instance.State),
	}, nil
}

func (f *Service) Bind(ctx context.Context, r attachmentsv1.BindRequest) (attachmentsv1.BindingRef, error) {
	core, err := f.core()
	if err != nil {
		return attachmentsv1.BindingRef{}, err
	}
	instance, err := core.GetServiceInstance(ctx, r.TenantID, r.ServiceInstanceID)
	if err != nil {
		return attachmentsv1.BindingRef{}, err
	}
	capabilities := []string{}
	err = core.Store.Transact(ctx, func(tx application.Tx) error {
		plan, ok := tx.GetPlan(instance.PlanID, instance.PlanVersion)
		if !ok {
			return domain.NewError(domain.CodeNotFound, "service plan not found")
		}
		for _, candidate := range []string{"read", "connect", "consume"} {
			for _, allowed := range plan.Capabilities {
				if allowed == candidate {
					capabilities = []string{candidate}
					return nil
				}
			}
		}
		if len(plan.Capabilities) > 0 {
			capabilities = []string{plan.Capabilities[0]}
		}
		return nil
	})
	if err != nil {
		return attachmentsv1.BindingRef{}, err
	}
	binding, snapshot, err := core.BindService(ctx, application.BindServiceRequest{
		TenantID: r.TenantID, ApplicationID: r.ApplicationID, EnvironmentID: r.EnvironmentID,
		InstanceID: r.ServiceInstanceID, Capabilities: capabilities,
		ActorID: r.ActorID, IdempotencyKey: r.IdempotencyKey,
	})
	if err != nil {
		return attachmentsv1.BindingRef{}, err
	}
	return attachmentsv1.BindingRef{BindingID: binding.ID, SnapshotID: snapshot.SnapshotID, State: string(binding.State)}, nil
}

func (f *Service) AddDomain(ctx context.Context, r attachmentsv1.DomainRequest) (attachmentsv1.DomainRef, error) {
	core, err := f.core()
	if err != nil {
		return attachmentsv1.DomainRef{}, err
	}
	var claim domain.DomainClaim
	if strings.TrimSpace(r.Hostname) == "" {
		claim, err = core.CreateGeneratedDomain(ctx, application.GeneratedDomainRequest{
			TenantID: r.TenantID, ApplicationID: r.ApplicationID, EnvironmentID: r.EnvironmentID,
			ActorID: r.ActorID, IdempotencyKey: r.IdempotencyKey,
		})
	} else {
		claim, err = core.ClaimCustomDomain(ctx, application.ClaimDomainRequest{
			TenantID: r.TenantID, ApplicationID: r.ApplicationID, EnvironmentID: r.EnvironmentID,
			Hostname: r.Hostname, ActorID: r.ActorID, IdempotencyKey: r.IdempotencyKey,
		})
	}
	if err != nil {
		return attachmentsv1.DomainRef{}, err
	}
	return attachmentsv1.DomainRef{DomainClaimID: claim.ID, Hostname: claim.Hostname, State: string(claim.State)}, nil
}

// Purge is intentionally outside the broad agent-facing v1 Service contract:
// callers must supply an approval reference that the core verifies against the
// tenant, actor, exact service instance, and canonical destructive plan before
// the provider is touched.
func (f *Service) Purge(ctx context.Context, r PurgeRequest) (attachmentsv1.ServiceInstanceRef, error) {
	core, err := f.core()
	if err != nil {
		return attachmentsv1.ServiceInstanceRef{}, err
	}
	instance, err := core.PurgeService(ctx, application.PurgeServiceRequest{
		TenantID: r.TenantID, InstanceID: r.ServiceInstanceID, ActorID: r.ActorID,
		PlanHash: r.PlanHash, ApprovalRef: r.ApprovalRef, IdempotencyKey: r.IdempotencyKey,
	})
	if err != nil {
		return attachmentsv1.ServiceInstanceRef{}, err
	}
	return attachmentsv1.ServiceInstanceRef{
		ServiceInstanceID: instance.ID, InstanceID: instance.ID, TenantID: instance.TenantID,
		PlanID: instance.PlanID, Type: instance.Type, State: string(instance.State),
	}, nil
}
