// Package oidc maps already cryptographically verified OIDC claims into the
// kernel's provider-neutral principal contract.
//
// Signature, expiry, nonce, and audience verification belong to the HTTP OIDC
// verifier in front of this mapper. The mapper intentionally ignores tenant
// claims; tenant context is derived from active kernel membership.
package oidc

import (
	"fmt"
	"strings"

	kernelv1 "github.com/keir-research/ai-native-paas/contracts/kernel/v1"
	"github.com/keir-research/ai-native-paas/internal/kernel"
)

type VerifiedClaims struct {
	Issuer  string
	Subject string
	Kind    string
	Scopes  []string
	Claims  map[string]any
}

type Mapper struct {
	Issuer        string
	PrincipalKind kernelv1.PrincipalKind
}

func (m Mapper) Map(claims VerifiedClaims) (kernelv1.PrincipalContext, error) {
	expectedIssuer := strings.TrimRight(strings.TrimSpace(m.Issuer), "/")
	actualIssuer := strings.TrimRight(strings.TrimSpace(claims.Issuer), "/")
	if expectedIssuer == "" {
		return kernelv1.PrincipalContext{}, kernel.NewError(kernelv1.CodeInvalidArgument, "OIDC issuer configuration is required")
	}
	if actualIssuer != expectedIssuer {
		return kernelv1.PrincipalContext{}, kernel.NewError(kernelv1.CodeForbidden, "OIDC issuer is not trusted")
	}
	subject := strings.TrimSpace(claims.Subject)
	if subject == "" {
		return kernelv1.PrincipalContext{}, kernel.NewError(kernelv1.CodeForbidden, "OIDC subject is required")
	}
	// Principal kind is verifier configuration, not a token-controlled claim.
	// Human OIDC defaults to user; service/workspace identities use mTLS.
	kind := m.PrincipalKind
	if kind == "" {
		kind = kernelv1.PrincipalKindUser
	}
	if !kind.Valid() {
		return kernelv1.PrincipalContext{}, kernel.NewError(kernelv1.CodeInvalidArgument, fmt.Sprintf("configured OIDC principal kind %q is invalid", kind))
	}
	principal := kernelv1.PrincipalContext{
		PrincipalID: kernelv1.PrincipalID("oidc:" + subject),
		Kind:        kind,
		Scopes:      normalizeScopes(claims.Scopes),
		// TenantID deliberately remains empty even if claims.Claims contains
		// tenant_id, organization_id, groups, or a similarly named field.
	}
	if err := principal.Validate(); err != nil {
		return kernelv1.PrincipalContext{}, kernel.NewError(kernelv1.CodeForbidden, "OIDC principal is invalid")
	}
	return principal, nil
}

func normalizeScopes(scopes []string) []string {
	seen := make(map[string]struct{}, len(scopes))
	normalized := make([]string, 0, len(scopes))
	for _, value := range scopes {
		for _, scope := range strings.Fields(value) {
			if _, exists := seen[scope]; exists {
				continue
			}
			seen[scope] = struct{}{}
			normalized = append(normalized, scope)
		}
	}
	return normalized
}
