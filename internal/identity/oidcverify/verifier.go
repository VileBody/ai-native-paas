// Package oidcverify cryptographically verifies human OIDC tokens and resolves controlled-beta membership.
package oidcverify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	coreoidc "github.com/coreos/go-oidc/v3/oidc"
	"github.com/keir-research/ai-native-paas/internal/identity/httpauth"
)

type TokenClaims struct {
	Issuer  string
	Subject string
	Scopes  []string
}

type TokenVerifier interface {
	Verify(context.Context, string) (TokenClaims, error)
}

type Membership struct {
	TenantID string
	UserID   string
	Role     string
}

type MembershipResolver interface {
	ResolveMembership(context.Context, string, string) (Membership, error)
}

type Verifier struct {
	Issuer      string
	Tokens      TokenVerifier
	Memberships MembershipResolver
}

func New(ctx context.Context, issuer, clientID string, memberships MembershipResolver) (*Verifier, error) {
	issuer = strings.TrimRight(strings.TrimSpace(issuer), "/")
	clientID = strings.TrimSpace(clientID)
	if issuer == "" || clientID == "" || memberships == nil {
		return nil, errors.New("OIDC issuer, client id, and membership resolver are required")
	}
	provider, err := coreoidc.NewProvider(ctx, issuer)
	if err != nil {
		return nil, fmt.Errorf("discover OIDC provider: %w", err)
	}
	return &Verifier{
		Issuer: issuer, Memberships: memberships,
		Tokens: coreTokenVerifier{verifier: provider.Verifier(&coreoidc.Config{ClientID: clientID})},
	}, nil
}

func (v *Verifier) VerifyOIDC(ctx context.Context, rawToken string) (httpauth.Identity, error) {
	if v == nil || v.Tokens == nil || v.Memberships == nil || strings.TrimSpace(rawToken) == "" {
		return httpauth.Identity{}, errors.New("OIDC verifier is unavailable")
	}
	claims, err := v.Tokens.Verify(ctx, rawToken)
	if err != nil {
		return httpauth.Identity{}, errors.New("OIDC token verification failed")
	}
	issuer := strings.TrimRight(strings.TrimSpace(claims.Issuer), "/")
	if issuer != strings.TrimRight(strings.TrimSpace(v.Issuer), "/") || strings.TrimSpace(claims.Subject) == "" {
		return httpauth.Identity{}, errors.New("OIDC token identity is invalid")
	}
	membership, err := v.Memberships.ResolveMembership(ctx, issuer, claims.Subject)
	if err != nil || membership.TenantID == "" || membership.UserID == "" {
		return httpauth.Identity{}, errors.New("OIDC subject has no unambiguous active beta membership")
	}
	scopes := normalizeScopes(claims.Scopes)
	return httpauth.Identity{
		SubjectID: membership.UserID, TenantID: membership.TenantID, UserID: membership.UserID,
		Scopes: scopes,
	}, nil
}

type coreTokenVerifier struct{ verifier *coreoidc.IDTokenVerifier }

func (v coreTokenVerifier) Verify(ctx context.Context, rawToken string) (TokenClaims, error) {
	token, err := v.verifier.Verify(ctx, rawToken)
	if err != nil {
		return TokenClaims{}, err
	}
	var extra struct {
		Scope scopeList `json:"scope"`
		SCP   scopeList `json:"scp"`
	}
	if err := token.Claims(&extra); err != nil {
		return TokenClaims{}, err
	}
	scopes := append([]string(nil), extra.Scope...)
	scopes = append(scopes, extra.SCP...)
	return TokenClaims{Issuer: token.Issuer, Subject: token.Subject, Scopes: scopes}, nil
}

type scopeList []string

func (s *scopeList) UnmarshalJSON(raw []byte) error {
	var list []string
	if err := json.Unmarshal(raw, &list); err == nil {
		*s = list
		return nil
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return errors.New("OIDC scope claim must be a string or string array")
	}
	*s = strings.Fields(value)
	return nil
}

func normalizeScopes(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		for _, scope := range strings.Fields(value) {
			if _, ok := seen[scope]; ok {
				continue
			}
			seen[scope] = struct{}{}
			out = append(out, scope)
		}
	}
	return out
}
