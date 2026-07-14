package openbao

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/keir-research/ai-native-paas/internal/workspace/bootstrap"
)

const openBaoTokenSentinel = "openbao-workload-token-never-leak"

type issuerClock struct{ now time.Time }

func (c issuerClock) Now() time.Time { return c.now }

func tokenFile(t *testing.T) string {
	t.Helper()
	filename := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(filename, []byte(openBaoTokenSentinel+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return filename
}

func certificateResponse(t *testing.T, now time.Time, identity *url.URL, usages []x509.ExtKeyUsage) map[string]any {
	t.Helper()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	caTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "workspace-test-ca"},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	leafTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "workspace-1"}, URIs: []*url.URL{identity},
		NotBefore: now.Add(-time.Minute), NotAfter: now.Add(14 * time.Minute),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: usages,
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTemplate, caTemplate, &leafKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	privateKey, err := x509.MarshalPKCS8PrivateKey(leafKey)
	if err != nil {
		t.Fatal(err)
	}
	return map[string]any{"data": map[string]any{
		"certificate": pemString("CERTIFICATE", leafDER), "private_key": pemString("PRIVATE KEY", privateKey),
		"ca_chain": []string{pemString("CERTIFICATE", caDER)}, "issuing_ca": pemString("CERTIFICATE", caDER),
		"serial_number": "02",
	}}
}

func signingResponse(t *testing.T, now time.Time, identity *url.URL, publicKey any) map[string]any {
	t.Helper()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	caTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(11), Subject: pkix.Name{CommonName: "workspace-signing-ca"},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	leafTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(12), URIs: []*url.URL{identity}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(14 * time.Minute),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTemplate, caTemplate, publicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	return map[string]any{"data": map[string]any{
		"certificate": pemString("CERTIFICATE", leafDER), "ca_chain": []string{pemString("CERTIFICATE", caDER)},
		"issuing_ca": pemString("CERTIFICATE", caDER), "serial_number": "0c",
	}}
}

func pemString(kind string, value []byte) string {
	return string(pem.EncodeToMemory(&pem.Block{Type: kind, Bytes: value}))
}

func TestOpenBaoIssuer_IssuesExactShortLivedClientSPIFFEIdentity(t *testing.T) {
	now := time.Date(2026, 7, 14, 14, 0, 0, 0, time.UTC)
	identity := bootstrap.Identity{TenantID: "tenant-1", ProjectID: "project-1", WorkspaceID: "workspace-1", TaskID: "task-1", AgentID: "agent-1"}
	expectedURI, err := identity.SPIFFEURI("workspace.platform.example.com")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/v1/workspace-pki/issue/workspace-agent" || request.Header.Get("Authorization") != "Bearer "+openBaoTokenSentinel {
			http.Error(response, "unexpected request", http.StatusForbidden)
			return
		}
		var payload struct {
			URISANs           string `json:"uri_sans"`
			ExcludeCNFromSANs bool   `json:"exclude_cn_from_sans"`
			TTL               string `json:"ttl"`
		}
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil || payload.URISANs != expectedURI.String() || !payload.ExcludeCNFromSANs || payload.TTL != "15m0s" {
			http.Error(response, "bad payload", http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(response).Encode(certificateResponse(t, now, expectedURI, []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}))
	}))
	defer server.Close()
	issuer, err := NewIssuer(Config{
		Address: server.URL, TokenFile: tokenFile(t), PKIMount: "workspace-pki", Role: "workspace-agent",
		TrustDomain: "workspace.platform.example.com", TTL: 15 * time.Minute, HTTPClient: server.Client(), Clock: issuerClock{now},
	})
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := issuer.Issue(context.Background(), identity)
	if err != nil {
		t.Fatal(err)
	}
	if bundle.IdentityURI != expectedURI.String() || bundle.Serial != "02" || !bundle.NotAfter.Equal(now.Add(14*time.Minute)) || len(bundle.PrivateKey) == 0 || len(bundle.Certificate) == 0 || len(bundle.CAChain) == 0 {
		t.Fatalf("invalid issued bundle: identity=%q serial=%q not_after=%v", bundle.IdentityURI, bundle.Serial, bundle.NotAfter)
	}
	bundle.Clear()
	if bundle.PrivateKey != nil || bundle.Certificate != nil || bundle.CAChain != nil {
		t.Fatal("cleared bootstrap bundle retained key material")
	}
}

func TestOpenBaoIssuer_RejectsWrongIdentityOrServerCertificateAndContainsErrors(t *testing.T) {
	now := time.Date(2026, 7, 14, 14, 30, 0, 0, time.UTC)
	identity := bootstrap.Identity{TenantID: "tenant-1", ProjectID: "project-1", WorkspaceID: "workspace-1", TaskID: "task-1", AgentID: "agent-1"}
	wrongURI, _ := url.Parse("spiffe://workspace.platform.example.com/tenant/tenant-1/project/foreign/workspace/workspace-1/task/task-1/agent/agent-1")
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		_ = json.NewEncoder(response).Encode(certificateResponse(t, now, wrongURI, []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth}))
	}))
	issuer, err := NewIssuer(Config{
		Address: server.URL, TokenFile: tokenFile(t), PKIMount: "workspace-pki", Role: "workspace-agent",
		TrustDomain: "workspace.platform.example.com", TTL: 15 * time.Minute, HTTPClient: server.Client(), Clock: issuerClock{now},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := issuer.Issue(context.Background(), identity); err == nil || strings.Contains(err.Error(), openBaoTokenSentinel) {
		t.Fatalf("wrong certificate accepted or token leaked: %v", err)
	}
	server.Close()

	failure := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.WriteHeader(http.StatusInternalServerError)
		_, _ = response.Write([]byte(`{"errors":["provider-body-secret ` + openBaoTokenSentinel + `"]}`))
	}))
	defer failure.Close()
	issuer.base, _ = url.Parse(failure.URL)
	issuer.client = failure.Client()
	if _, err := issuer.Issue(context.Background(), identity); err == nil || strings.Contains(err.Error(), openBaoTokenSentinel) || strings.Contains(err.Error(), "provider-body-secret") {
		t.Fatalf("provider response leaked: %v", err)
	}
}

func TestOpenBaoIssuer_RevokesExactLeaseSynchronouslyWithoutLeakingProviderBody(t *testing.T) {
	now := time.Date(2026, 7, 14, 15, 0, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/sys/leases/revoke" || request.Header.Get("Authorization") != "Bearer "+openBaoTokenSentinel {
			http.Error(response, "bad request", http.StatusForbidden)
			return
		}
		var payload struct {
			LeaseID string `json:"lease_id"`
			Sync    bool   `json:"sync"`
		}
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil || payload.LeaseID != "database/creds/project/lease-1" || !payload.Sync {
			http.Error(response, "bad payload", http.StatusBadRequest)
			return
		}
		response.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	issuer, err := NewIssuer(Config{
		Address: server.URL, TokenFile: tokenFile(t), PKIMount: "workspace-pki", Role: "workspace-agent",
		TrustDomain: "workspace.platform.example.com", TTL: 15 * time.Minute, HTTPClient: server.Client(), Clock: issuerClock{now},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := issuer.Revoke(context.Background(), "database/creds/project/lease-1", "command_terminal"); err != nil {
		t.Fatal(err)
	}
}

func TestOpenBaoIssuer_RotationSignsAgentGeneratedKeyForSameSPIFFEIdentity(t *testing.T) {
	now := time.Date(2026, 7, 14, 15, 30, 0, 0, time.UTC)
	identity := bootstrap.Identity{TenantID: "tenant-1", ProjectID: "project-1", WorkspaceID: "workspace-1", TaskID: "task-1", AgentID: "agent-1"}
	expectedURI, _ := identity.SPIFFEURI("workspace.platform.example.com")
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	csrDER, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{}, key)
	if err != nil {
		t.Fatal(err)
	}
	csrPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csrDER})
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/workspace-pki/sign/workspace-agent" || request.Header.Get("Authorization") != "Bearer "+openBaoTokenSentinel {
			http.Error(response, "bad request", http.StatusForbidden)
			return
		}
		var payload struct {
			CSR               string `json:"csr"`
			URISANs           string `json:"uri_sans"`
			ExcludeCNFromSANs bool   `json:"exclude_cn_from_sans"`
			TTL               string `json:"ttl"`
		}
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil || payload.CSR != string(csrPEM) || payload.URISANs != expectedURI.String() || !payload.ExcludeCNFromSANs || payload.TTL != "15m0s" {
			http.Error(response, "bad payload", http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(response).Encode(signingResponse(t, now, expectedURI, &key.PublicKey))
	}))
	defer server.Close()
	issuer, err := NewIssuer(Config{
		Address: server.URL, TokenFile: tokenFile(t), PKIMount: "workspace-pki", Role: "workspace-agent",
		TrustDomain: "workspace.platform.example.com", TTL: 15 * time.Minute, HTTPClient: server.Client(), Clock: issuerClock{now},
	})
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := issuer.Sign(context.Background(), identity, csrPEM)
	if err != nil {
		t.Fatal(err)
	}
	if len(bundle.PrivateKey) != 0 || bundle.IdentityURI != expectedURI.String() || bundle.Serial != "0c" || !bundle.NotAfter.Equal(now.Add(14*time.Minute)) {
		t.Fatalf("invalid signed bundle: %#v", bundle)
	}

	badCSR, _ := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{DNSNames: []string{"attacker.example.com"}}, key)
	if _, err := issuer.Sign(context.Background(), identity, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: badCSR})); err == nil {
		t.Fatal("CSR-controlled SAN accepted")
	}
}
