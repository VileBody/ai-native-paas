// Package httpauth authenticates production requests before legacy handlers can inspect identity headers.
package httpauth

import (
	"context"
	"crypto/x509"
	"errors"
	"net/http"
	"strings"

	kernelv2 "github.com/keir-research/ai-native-paas/contracts/kernel/v2"
	"github.com/keir-research/ai-native-paas/internal/platformprofile"
)

type Identity struct {
	SubjectID string
	TenantID  string
	ProjectID string
	UserID    string
	AgentID   string
	Scopes    []string
	KindClaim string
	Source    string
}

type OIDCVerifier interface {
	VerifyOIDC(context.Context, string) (Identity, error)
}

type MTLSVerifier interface {
	VerifyMTLS(context.Context, *x509.Certificate) (Identity, error)
}

type Middleware struct {
	Profile     platformprofile.Profile
	OIDC        OIDCVerifier
	MTLS        MTLSVerifier
	PublicPaths map[string]struct{}
}

type contextKey struct{}

func IdentityFromContext(ctx context.Context) (Identity, bool) {
	identity, ok := ctx.Value(contextKey{}).(Identity)
	return identity, ok
}

var developmentIdentityHeaders = []string{"X-Tenant-ID", "X-Project-ID", "X-Principal-ID", "X-Principal-Kind", "X-Agent-ID", "X-User-ID", "X-Scopes"}

func (m Middleware) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, public := m.PublicPaths[r.URL.Path]; public {
			next.ServeHTTP(w, r)
			return
		}
		if m.Profile != platformprofile.Production {
			next.ServeHTTP(w, r)
			return
		}
		for _, header := range developmentIdentityHeaders {
			if strings.TrimSpace(r.Header.Get(header)) != "" {
				unauthorized(w)
				return
			}
		}
		identity, err := m.authenticate(r)
		if err != nil {
			unauthorized(w)
			return
		}
		request := r.Clone(context.WithValue(r.Context(), contextKey{}, identity))
		request.Header = r.Header.Clone()
		request.Header.Set("X-Tenant-ID", identity.TenantID)
		request.Header.Set("X-Project-ID", identity.ProjectID)
		request.Header.Set("X-Principal-ID", identity.SubjectID)
		request.Header.Set("X-Principal-Kind", strings.ToLower(identity.KindClaim))
		request.Header.Set("X-Agent-ID", identity.AgentID)
		request.Header.Set("X-User-ID", identity.UserID)
		request.Header.Set("X-Scopes", strings.Join(identity.Scopes, " "))
		next.ServeHTTP(w, request)
	})
}

func (m Middleware) authenticate(r *http.Request) (Identity, error) {
	bearer := strings.TrimSpace(r.Header.Get("Authorization"))
	var certificate *x509.Certificate
	if r.TLS != nil && len(r.TLS.PeerCertificates) > 0 {
		certificate = r.TLS.PeerCertificates[0]
	}
	if bearer != "" && certificate != nil {
		return Identity{}, errors.New("ambiguous OIDC and mTLS identity")
	}
	var (
		identity Identity
		err      error
		kind     kernelv2.PrincipalKind
	)
	switch {
	case strings.HasPrefix(bearer, "Bearer ") && m.OIDC != nil:
		identity, err = m.OIDC.VerifyOIDC(r.Context(), strings.TrimSpace(strings.TrimPrefix(bearer, "Bearer ")))
		kind = kernelv2.PrincipalUser
		identity.Source = "oidc"
	case certificate != nil && m.MTLS != nil:
		identity, err = m.MTLS.VerifyMTLS(r.Context(), certificate)
		kind = kernelv2.PrincipalService
		identity.Source = "mtls"
	default:
		return Identity{}, errors.New("verified OIDC or mTLS identity is required")
	}
	if err != nil || strings.TrimSpace(identity.SubjectID) == "" || strings.TrimSpace(identity.TenantID) == "" {
		return Identity{}, errors.New("identity verification failed")
	}
	// Principal kind is selected by the trusted verification path. Any kind-like
	// claim returned by an upstream token/certificate parser is overwritten.
	identity.KindClaim = string(kind)
	return identity, nil
}

func unauthorized(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusUnauthorized)
	_, _ = w.Write([]byte(`{"code":"UNAUTHENTICATED","message":"verified OIDC or mTLS identity is required"}`))
}
