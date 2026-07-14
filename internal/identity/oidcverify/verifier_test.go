package oidcverify

import (
	"context"
	"errors"
	"testing"
)

type tokenVerifierStub struct {
	claims TokenClaims
	err    error
}

func (v tokenVerifierStub) Verify(context.Context, string) (TokenClaims, error) {
	return v.claims, v.err
}

type membershipResolverStub struct {
	membership Membership
	err        error
}

func (r membershipResolverStub) ResolveMembership(context.Context, string, string) (Membership, error) {
	return r.membership, r.err
}

func TestOIDCVerifier_UsesServerSideMembershipAndNormalizesScopes(t *testing.T) {
	verifier := Verifier{
		Issuer: "https://gitlab.com", Tokens: tokenVerifierStub{claims: TokenClaims{Issuer: "https://gitlab.com/", Subject: "gitlab-user-1", Scopes: []string{"openid profile", "profile"}}},
		Memberships: membershipResolverStub{membership: Membership{TenantID: "tenant-1", UserID: "user-1", Role: "OWNER"}},
	}
	identity, err := verifier.VerifyOIDC(context.Background(), "signed-token")
	if err != nil {
		t.Fatal(err)
	}
	if identity.TenantID != "tenant-1" || identity.UserID != "user-1" || identity.SubjectID != "user-1" || len(identity.Scopes) != 2 {
		t.Fatalf("identity=%#v", identity)
	}
}

func TestOIDCVerifier_RejectsWrongIssuerAndAmbiguousMembership(t *testing.T) {
	for _, test := range []Verifier{
		{Issuer: "https://gitlab.com", Tokens: tokenVerifierStub{claims: TokenClaims{Issuer: "https://evil.invalid", Subject: "user"}}, Memberships: membershipResolverStub{membership: Membership{TenantID: "tenant-1", UserID: "user-1"}}},
		{Issuer: "https://gitlab.com", Tokens: tokenVerifierStub{claims: TokenClaims{Issuer: "https://gitlab.com", Subject: "user"}}, Memberships: membershipResolverStub{err: errors.New("ambiguous")}},
	} {
		if _, err := test.VerifyOIDC(context.Background(), "signed-token"); err == nil {
			t.Fatal("untrusted OIDC identity was accepted")
		}
	}
}
