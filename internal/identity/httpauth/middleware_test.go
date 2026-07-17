package httpauth

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/keir-research/ai-native-paas/internal/platformprofile"
)

type oidcVerifierFunc func(context.Context, string) (Identity, error)

func (f oidcVerifierFunc) VerifyOIDC(ctx context.Context, token string) (Identity, error) {
	return f(ctx, token)
}

type mtlsVerifierFunc func(context.Context, *x509.Certificate) (Identity, error)

func (f mtlsVerifierFunc) VerifyMTLS(ctx context.Context, certificate *x509.Certificate) (Identity, error) {
	return f(ctx, certificate)
}

func TestKernel_ProductionProfileRejectsDevelopmentIdentityHeaders(t *testing.T) {
	var invoked bool
	handler := Middleware{Profile: platformprofile.Production}.Wrap(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { invoked = true }))
	request := httptest.NewRequest(http.MethodGet, "/v2/operations", nil)
	request.Header.Set("X-Tenant-ID", "tenant-1")
	request.Header.Set("X-Principal-ID", "user-1")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized || invoked {
		t.Fatalf("development headers reached handler: status=%d invoked=%t", response.Code, invoked)
	}
}

func TestKernel_OIDCAndMTLSIdentityCannotBeConfused(t *testing.T) {
	oidc := oidcVerifierFunc(func(_ context.Context, token string) (Identity, error) {
		return Identity{SubjectID: "user-1", TenantID: "tenant-1", ProjectID: "project-1", KindClaim: "WORKSPACE", Scopes: []string{"project:read"}}, nil
	})
	mtls := mtlsVerifierFunc(func(_ context.Context, _ *x509.Certificate) (Identity, error) {
		return Identity{SubjectID: "workspace-1", TenantID: "tenant-1", ProjectID: "project-1", KindClaim: "USER", Scopes: []string{"workspace:exec"}}, nil
	})
	verified := make(chan Identity, 2)
	handler := Middleware{Profile: platformprofile.Production, OIDC: oidc, MTLS: mtls}.Wrap(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		identity, ok := IdentityFromContext(request.Context())
		if !ok {
			t.Error("trusted identity missing from context")
			return
		}
		verified <- identity
	}))

	oidcRequest := httptest.NewRequest(http.MethodGet, "/v2/operations", nil)
	oidcRequest.Header.Set("Authorization", "Bearer signed-user-token")
	oidcResponse := httptest.NewRecorder()
	handler.ServeHTTP(oidcResponse, oidcRequest)
	if identity := <-verified; identity.KindClaim != "USER" || identity.Source != "oidc" {
		t.Fatalf("OIDC claim confused principal kind: %#v", identity)
	}

	mtlsRequest := httptest.NewRequest(http.MethodGet, "/v2/operations", nil)
	mtlsRequest.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{{SerialNumber: nil}}}
	mtlsResponse := httptest.NewRecorder()
	handler.ServeHTTP(mtlsResponse, mtlsRequest)
	if identity := <-verified; identity.KindClaim != "SERVICE" || identity.Source != "mtls" {
		t.Fatalf("mTLS claim confused principal kind: %#v", identity)
	}

	ambiguous := httptest.NewRequest(http.MethodGet, "/v2/operations", nil)
	ambiguous.Header.Set("Authorization", "Bearer token")
	ambiguous.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{{}}}
	ambiguousResponse := httptest.NewRecorder()
	handler.ServeHTTP(ambiguousResponse, ambiguous)
	if ambiguousResponse.Code != http.StatusUnauthorized {
		t.Fatalf("ambiguous identity accepted: %d", ambiguousResponse.Code)
	}
}

func TestAgent_APIRequiresOIDCOrMTLSAndRejectsIdentityHeadersInProduction(t *testing.T) {
	var invoked bool
	handler := Middleware{Profile: platformprofile.Production}.Wrap(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { invoked = true }))
	requests := []*http.Request{httptest.NewRequest(http.MethodPost, "/mcp/v2/invoke", nil)}
	for _, header := range developmentIdentityHeaders {
		request := httptest.NewRequest(http.MethodPost, "/mcp/v2/invoke", nil)
		request.Header.Set(header, "forged")
		requests = append(requests, request)
	}
	for _, request := range requests {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("unverified request accepted: status=%d", response.Code)
		}
	}
	if invoked {
		t.Fatal("unverified agent request reached tool dispatch")
	}
}

func TestProductionOIDCAndMTLSRejectForgedPrincipalRoleBeforeDispatch(t *testing.T) {
	var invoked bool
	verified := Middleware{
		Profile: platformprofile.Production,
		OIDC: oidcVerifierFunc(func(_ context.Context, _ string) (Identity, error) {
			return Identity{SubjectID: "user-1", TenantID: "tenant-1", UserID: "user-1"}, nil
		}),
		MTLS: mtlsVerifierFunc(func(_ context.Context, _ *x509.Certificate) (Identity, error) {
			return Identity{SubjectID: "service-1", TenantID: "tenant-1", AgentID: "agent-1"}, nil
		}),
	}.Wrap(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { invoked = true }))

	oidcRequest := httptest.NewRequest(http.MethodPost, "/v1/admin/rate-cards", nil)
	oidcRequest.Header.Set("Authorization", "Bearer valid-user-token")
	oidcRequest.Header.Set("X-Principal-Role", "platform-admin")
	oidcResponse := httptest.NewRecorder()
	verified.ServeHTTP(oidcResponse, oidcRequest)

	mtlsRequest := httptest.NewRequest(http.MethodPost, "/v1/admin/rate-cards", nil)
	mtlsRequest.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{{}}}
	mtlsRequest.Header.Set("X-Principal-Role", "platform-admin")
	mtlsResponse := httptest.NewRecorder()
	verified.ServeHTTP(mtlsResponse, mtlsRequest)

	if oidcResponse.Code != http.StatusUnauthorized || mtlsResponse.Code != http.StatusUnauthorized || invoked {
		t.Fatalf("forged role reached dispatch: oidc=%d mtls=%d invoked=%t", oidcResponse.Code, mtlsResponse.Code, invoked)
	}
}

func TestProductionMTLSMayDelegateOnlyTenantAndProjectScope(t *testing.T) {
	verified := Middleware{
		Profile: platformprofile.Production,
		MTLS: mtlsVerifierFunc(func(_ context.Context, _ *x509.Certificate) (Identity, error) {
			return Identity{SubjectID: "agent-api", Scopes: []string{"runtime:write"}}, nil
		}),
	}.Wrap(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		identity, ok := IdentityFromContext(request.Context())
		if !ok || identity.SubjectID != "agent-api" || identity.TenantID != "tenant-1" || identity.ProjectID != "project-1" || identity.KindClaim != "SERVICE" {
			t.Fatalf("delegated identity=%+v ok=%t", identity, ok)
		}
		if request.Header.Get("X-Principal-ID") != "agent-api" || request.Header.Get("X-Tenant-ID") != "tenant-1" || request.Header.Get("X-Project-ID") != "project-1" {
			t.Fatalf("trusted headers=%v", request.Header)
		}
		w.WriteHeader(http.StatusNoContent)
	}))

	request := httptest.NewRequest(http.MethodPost, "/v1/organizations/tenant-1/applications", nil)
	request.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{{}}}
	request.Header.Set("X-Tenant-ID", "tenant-1")
	request.Header.Set("X-Project-ID", "project-1")
	response := httptest.NewRecorder()
	verified.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("delegated request status=%d body=%s", response.Code, response.Body.String())
	}

	forged := httptest.NewRequest(http.MethodPost, "/v1/organizations/tenant-1/applications", nil)
	forged.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{{}}}
	forged.Header.Set("X-Tenant-ID", "tenant-1")
	forged.Header.Set("X-Principal-ID", "attacker")
	forgedResponse := httptest.NewRecorder()
	verified.ServeHTTP(forgedResponse, forged)
	if forgedResponse.Code != http.StatusUnauthorized {
		t.Fatalf("forged principal accepted: %d", forgedResponse.Code)
	}
}

func TestProductionMTLSCertificateScopeCannotBeOverridden(t *testing.T) {
	handler := Middleware{
		Profile: platformprofile.Production,
		MTLS: mtlsVerifierFunc(func(_ context.Context, _ *x509.Certificate) (Identity, error) {
			return Identity{SubjectID: "workspace-agent", TenantID: "tenant-bound", ProjectID: "project-bound"}, nil
		}),
	}.Wrap(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("conflicting scope reached handler") }))

	request := httptest.NewRequest(http.MethodPost, "/mcp/v2/invoke", nil)
	request.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{{}}}
	request.Header.Set("X-Tenant-ID", "tenant-other")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("conflicting certificate scope accepted: %d", response.Code)
	}
}

func TestProductionPublicPrefixesAreExplicit(t *testing.T) {
	var invoked bool
	handler := Middleware{
		Profile:        platformprofile.Production,
		PublicPrefixes: []string{"/hooks/gitlab/"},
	}.Wrap(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { invoked = true }))

	allowed := httptest.NewRequest(http.MethodPost, "/hooks/gitlab/tenant-1", nil)
	handler.ServeHTTP(httptest.NewRecorder(), allowed)
	if !invoked {
		t.Fatal("configured webhook prefix was not public")
	}

	invoked = false
	lookalike := httptest.NewRequest(http.MethodPost, "/hooks/gitlab-evil/tenant-1", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, lookalike)
	if invoked || response.Code != http.StatusUnauthorized {
		t.Fatalf("lookalike prefix bypassed authentication: invoked=%t status=%d", invoked, response.Code)
	}
}
