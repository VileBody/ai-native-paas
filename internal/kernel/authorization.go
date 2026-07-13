package kernel

import (
	"context"
	"strings"

	kernelv1 "github.com/keir-research/ai-native-paas/contracts/kernel/v1"
)

const (
	ActionOrganizationCreate = "kernel.organization.create"
	ActionOrganizationRead   = "kernel.organization.read"
	ActionMembershipInvite   = "kernel.membership.invite"
	ActionMembershipAccept   = "kernel.membership.accept"
	ActionMembershipManage   = "kernel.membership.manage"
	ActionAuditRead          = "kernel.audit.read"
	ActionOperationStart     = "kernel.operation.start"
	ActionOperationCancel    = "kernel.operation.cancel"
	ActionOperationRead      = "kernel.operation.read"
)

var roleActions = map[MembershipRole]map[string]struct{}{
	RoleOwner: {
		ActionOrganizationRead: {}, ActionMembershipInvite: {}, ActionMembershipManage: {},
		ActionAuditRead: {}, ActionOperationStart: {}, ActionOperationCancel: {}, ActionOperationRead: {},
	},
	RoleAdmin: {
		ActionOrganizationRead: {}, ActionMembershipInvite: {}, ActionMembershipManage: {},
		ActionAuditRead: {}, ActionOperationStart: {}, ActionOperationCancel: {}, ActionOperationRead: {},
	},
	RoleDeveloper: {
		ActionOrganizationRead: {}, ActionOperationStart: {}, ActionOperationRead: {},
	},
	RoleViewer: {
		ActionOrganizationRead: {}, ActionOperationRead: {},
	},
}

type AuthorizationPolicy struct{}

func (AuthorizationPolicy) Check(principal kernelv1.PrincipalContext, action string, resource kernelv1.ResourceRef, organization *Organization) error {
	if err := principal.Validate(); err != nil {
		return forbidden("invalid principal")
	}
	if err := resource.Validate(); err != nil {
		return forbidden("invalid resource context")
	}
	if strings.TrimSpace(action) == "" || !principal.HasScope(action) {
		return forbidden("principal does not have the required scope")
	}
	if principal.TenantID == "" || principal.TenantID != resource.TenantID {
		return forbidden("cross-tenant access is denied")
	}
	if organization == nil || organization.ID != resource.TenantID {
		return forbidden("tenant resource is unavailable")
	}
	membership, ok := organization.Memberships[principal.PrincipalID]
	if !ok || membership.State != MembershipActive {
		return forbidden("active membership is required")
	}
	allowed, roleExists := roleActions[membership.Role]
	if !roleExists {
		return forbidden("membership role is not recognized")
	}
	if _, ok := allowed[action]; !ok {
		return forbidden("membership role does not permit the action")
	}
	return nil
}

// StoreAuthorizer implements the frozen v1 Authorizer port.
type StoreAuthorizer struct {
	Store  Store
	Policy AuthorizationPolicy
}

func (a StoreAuthorizer) Check(ctx context.Context, principal kernelv1.PrincipalContext, action string, resource kernelv1.ResourceRef) error {
	if a.Store == nil {
		return WrapError(kernelv1.CodeInternal, "Authorization service unavailable", true, nil)
	}
	return a.Store.View(ctx, func(reader Reader) error {
		organization, err := reader.GetOrganization(ctx, resource.TenantID)
		if err != nil {
			return err
		}
		return a.Policy.Check(principal, action, resource, organization)
	})
}
