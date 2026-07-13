package kernel

import (
	"testing"

	kernelv1 "github.com/keir-research/ai-native-paas/contracts/kernel/v1"
)

func TestMembership_InvitationDoesNotGrantAccess(t *testing.T) {
	organization, _ := NewOrganization("org_1", "Acme", "owner", testNow)
	if err := organization.Invite("developer", RoleDeveloper, testNow); err != nil {
		t.Fatal(err)
	}
	principal := kernelv1.PrincipalContext{PrincipalID: "developer", TenantID: "org_1", Kind: kernelv1.PrincipalKindUser, Scopes: []string{ActionOrganizationRead}}
	err := (AuthorizationPolicy{}).Check(principal, ActionOrganizationRead, organizationResource("org_1"), organization)
	if ErrorCode(err) != kernelv1.CodeForbidden {
		t.Fatalf("authorization error = %v, want FORBIDDEN", err)
	}
}

func TestMembership_AcceptanceGrantsConfiguredRole(t *testing.T) {
	organization, _ := NewOrganization("org_1", "Acme", "owner", testNow)
	_ = organization.Invite("developer", RoleDeveloper, testNow)
	_ = organization.AcceptInvitation("developer", testNow)
	principal := kernelv1.PrincipalContext{PrincipalID: "developer", TenantID: "org_1", Kind: kernelv1.PrincipalKindUser, Scopes: []string{ActionOrganizationRead}}
	if err := (AuthorizationPolicy{}).Check(principal, ActionOrganizationRead, organizationResource("org_1"), organization); err != nil {
		t.Fatalf("authorization failed: %v", err)
	}
}

func TestAuthorization_DeniesCrossTenantAccess(t *testing.T) {
	organization, _ := NewOrganization("org_b", "Beta", "user", testNow)
	principal := kernelv1.PrincipalContext{PrincipalID: "user", TenantID: "org_a", Kind: kernelv1.PrincipalKindUser, Scopes: []string{"kernel:*"}}
	err := (AuthorizationPolicy{}).Check(principal, ActionOrganizationRead, organizationResource("org_b"), organization)
	if ErrorCode(err) != kernelv1.CodeForbidden {
		t.Fatalf("authorization error = %v, want FORBIDDEN", err)
	}
}

func TestAuthorization_DeniesMissingScope(t *testing.T) {
	organization, _ := NewOrganization("org_1", "Acme", "owner", testNow)
	principal := kernelv1.PrincipalContext{PrincipalID: "owner", TenantID: "org_1", Kind: kernelv1.PrincipalKindUser, Scopes: []string{"profile:read"}}
	err := (AuthorizationPolicy{}).Check(principal, ActionMembershipInvite, membershipResource("org_1", "developer"), organization)
	if ErrorCode(err) != kernelv1.CodeForbidden {
		t.Fatalf("authorization error = %v, want FORBIDDEN", err)
	}
}

func TestAuthorization_AllowsOwnerScope(t *testing.T) {
	organization, _ := NewOrganization("org_1", "Acme", "owner", testNow)
	principal := kernelv1.PrincipalContext{PrincipalID: "owner", TenantID: "org_1", Kind: kernelv1.PrincipalKindUser, Scopes: []string{"kernel:*"}}
	for _, action := range []string{ActionOrganizationRead, ActionMembershipInvite, ActionMembershipManage, ActionAuditRead, ActionOperationStart, ActionOperationCancel, ActionOperationRead} {
		if err := (AuthorizationPolicy{}).Check(principal, action, kernelv1.ResourceRef{TenantID: "org_1", Type: "resource", ID: "1"}, organization); err != nil {
			t.Errorf("owner denied %s: %v", action, err)
		}
	}
}

func TestMembership_SuspendedPrincipalIsDenied(t *testing.T) {
	organization, _ := NewOrganization("org_1", "Acme", "owner", testNow)
	_ = organization.Invite("developer", RoleDeveloper, testNow)
	_ = organization.AcceptInvitation("developer", testNow)
	_ = organization.SuspendMember("developer", testNow)
	principal := kernelv1.PrincipalContext{PrincipalID: "developer", TenantID: "org_1", Kind: kernelv1.PrincipalKindUser, Scopes: []string{"kernel:*"}}
	err := (AuthorizationPolicy{}).Check(principal, ActionOrganizationRead, organizationResource("org_1"), organization)
	if ErrorCode(err) != kernelv1.CodeForbidden {
		t.Fatalf("authorization error = %v, want FORBIDDEN", err)
	}
}

func TestAuthorization_DeveloperCannotManageMembers(t *testing.T) {
	organization, _ := NewOrganization("org_1", "Acme", "owner", testNow)
	_ = organization.Invite("developer", RoleDeveloper, testNow)
	_ = organization.AcceptInvitation("developer", testNow)
	principal := kernelv1.PrincipalContext{PrincipalID: "developer", TenantID: "org_1", Kind: kernelv1.PrincipalKindUser, Scopes: []string{"kernel:*"}}
	err := (AuthorizationPolicy{}).Check(principal, ActionMembershipManage, membershipResource("org_1", "owner"), organization)
	if ErrorCode(err) != kernelv1.CodeForbidden {
		t.Fatalf("authorization error = %v, want FORBIDDEN", err)
	}
}

func organizationResource(id kernelv1.TenantID) kernelv1.ResourceRef {
	return kernelv1.ResourceRef{TenantID: id, Type: "organization", ID: string(id)}
}

func membershipResource(id kernelv1.TenantID, principal string) kernelv1.ResourceRef {
	return kernelv1.ResourceRef{TenantID: id, Type: "membership", ID: principal}
}
