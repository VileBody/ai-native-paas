package kernel

import (
	"testing"
	"time"

	kernelv1 "github.com/keir-research/ai-native-paas/contracts/kernel/v1"
)

var testNow = time.Date(2026, 7, 12, 10, 30, 0, 0, time.UTC)

func TestOrganization_Create_AssignsCreatorAsOwner(t *testing.T) {
	organization, err := NewOrganization("org_1", "Acme", "user_1", testNow)
	if err != nil {
		t.Fatalf("NewOrganization: %v", err)
	}
	membership, ok := organization.Membership("user_1")
	if !ok {
		t.Fatal("creator membership not found")
	}
	if membership.Role != RoleOwner || membership.State != MembershipActive {
		t.Fatalf("creator membership = %s/%s, want owner/ACTIVE", membership.Role, membership.State)
	}
	if organization.Version != 1 {
		t.Fatalf("version = %d, want 1", organization.Version)
	}
}

func TestOrganization_Create_RejectsEmptyName(t *testing.T) {
	for _, name := range []string{"", " ", "\n\t"} {
		if _, err := NewOrganization("org_1", name, "user_1", testNow); ErrorCode(err) != kernelv1.CodeInvalidArgument {
			t.Fatalf("NewOrganization(%q) error = %v, want INVALID_ARGUMENT", name, err)
		}
	}
}

func TestOrganization_Create_NormalizesSlug(t *testing.T) {
	tests := map[string]string{
		" Acme, Inc. ":         "acme-inc",
		"Hello___WORLD":        "hello-world",
		"many    separators":   "many-separators",
		"Привет, Мир":          "привет-мир",
		"version 2.0 / stable": "version-2-0-stable",
	}
	for input, want := range tests {
		t.Run(input, func(t *testing.T) {
			if got := NormalizeSlug(input); got != want {
				t.Fatalf("NormalizeSlug(%q) = %q, want %q", input, got, want)
			}
		})
	}
}

func TestOrganization_CannotRemoveLastOwner(t *testing.T) {
	organization, err := NewOrganization("org_1", "Acme", "owner_1", testNow)
	if err != nil {
		t.Fatal(err)
	}
	if err := organization.RemoveMember("owner_1", testNow.Add(time.Minute)); ErrorCode(err) != kernelv1.CodeLastOwner {
		t.Fatalf("RemoveMember error = %v, want LAST_OWNER_REQUIRED", err)
	}
	if err := organization.SuspendMember("owner_1", testNow.Add(time.Minute)); ErrorCode(err) != kernelv1.CodeLastOwner {
		t.Fatalf("SuspendMember error = %v, want LAST_OWNER_REQUIRED", err)
	}
	if err := organization.ChangeMemberRole("owner_1", RoleAdmin, testNow.Add(time.Minute)); ErrorCode(err) != kernelv1.CodeLastOwner {
		t.Fatalf("ChangeMemberRole error = %v, want LAST_OWNER_REQUIRED", err)
	}
}

func TestOrganization_CanRemoveOwnerWhenAnotherActiveOwnerRemains(t *testing.T) {
	organization, _ := NewOrganization("org_1", "Acme", "owner_1", testNow)
	if err := organization.Invite("owner_2", RoleOwner, testNow.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := organization.AcceptInvitation("owner_2", testNow.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := organization.RemoveMember("owner_1", testNow.Add(3*time.Minute)); err != nil {
		t.Fatalf("RemoveMember: %v", err)
	}
	first, _ := organization.Membership("owner_1")
	second, _ := organization.Membership("owner_2")
	if first.State != MembershipRemoved || second.State != MembershipActive || second.Role != RoleOwner {
		t.Fatalf("unexpected owner states: first=%+v second=%+v", first, second)
	}
}

func TestOrganization_ReinviteRemovedMembership(t *testing.T) {
	organization, _ := NewOrganization("org_1", "Acme", "owner_1", testNow)
	if err := organization.Invite("dev_1", RoleDeveloper, testNow); err != nil {
		t.Fatal(err)
	}
	if err := organization.RemoveMember("dev_1", testNow); err != nil {
		t.Fatal(err)
	}
	if err := organization.Invite("dev_1", RoleViewer, testNow.Add(time.Minute)); err != nil {
		t.Fatalf("reinvite: %v", err)
	}
	membership, _ := organization.Membership("dev_1")
	if membership.State != MembershipInvited || membership.Role != RoleViewer {
		t.Fatalf("membership = %+v", membership)
	}
}
