package oidc

import (
	"testing"

	kernelv1 "github.com/keir-research/ai-native-paas/contracts/kernel/v1"
	"github.com/keir-research/ai-native-paas/internal/kernel"
)

func TestOIDCClaims_MapsSubjectToPrincipal(t *testing.T) {
	principal, err := (Mapper{Issuer: "https://identity.example.com/"}).Map(VerifiedClaims{
		Issuer:  "https://identity.example.com",
		Subject: "user-123",
		Kind:    "user",
		Scopes:  []string{"kernel:* profile:read", "kernel:*"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if principal.PrincipalID != "oidc:user-123" || principal.Kind != kernelv1.PrincipalKindUser {
		t.Fatalf("principal = %+v", principal)
	}
	if len(principal.Scopes) != 2 || principal.Scopes[0] != "kernel:*" || principal.Scopes[1] != "profile:read" {
		t.Fatalf("normalized scopes = %v", principal.Scopes)
	}
}

func TestOIDCClaims_RejectsMissingSubject(t *testing.T) {
	_, err := (Mapper{Issuer: "https://identity.example.com"}).Map(VerifiedClaims{Issuer: "https://identity.example.com"})
	if kernel.ErrorCode(err) != kernelv1.CodeForbidden {
		t.Fatalf("error = %v, want FORBIDDEN", err)
	}
}

func TestOIDCClaims_RejectsWrongIssuer(t *testing.T) {
	_, err := (Mapper{Issuer: "https://identity.example.com"}).Map(VerifiedClaims{Issuer: "https://evil.example.com", Subject: "user-123"})
	if kernel.ErrorCode(err) != kernelv1.CodeForbidden {
		t.Fatalf("error = %v, want FORBIDDEN", err)
	}
}

func TestOIDCClaims_DoesNotTrustTenantFromUnsignedInput(t *testing.T) {
	principal, err := (Mapper{Issuer: "https://identity.example.com"}).Map(VerifiedClaims{
		Issuer:  "https://identity.example.com",
		Subject: "user-123",
		Claims: map[string]any{
			"tenant_id":       "org-victim",
			"organization_id": "org-victim",
			"groups":          []string{"owners"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if principal.TenantID != "" {
		t.Fatalf("untrusted tenant claim was accepted: %q", principal.TenantID)
	}
}

func TestOIDCClaims_RejectsUnknownPrincipalKind(t *testing.T) {
	_, err := (Mapper{Issuer: "https://identity.example.com"}).Map(VerifiedClaims{Issuer: "https://identity.example.com", Subject: "x", Kind: "root"})
	if kernel.ErrorCode(err) != kernelv1.CodeForbidden {
		t.Fatalf("error = %v", err)
	}
}
