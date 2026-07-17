package servicemtls

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/keir-research/ai-native-paas/internal/identity/httpauth"
)

func serviceCertificate(t *testing.T, rawURI string, now time.Time) *x509.Certificate {
	t.Helper()
	identity, err := url.Parse(rawURI)
	if err != nil {
		t.Fatal(err)
	}
	return &x509.Certificate{
		URIs: [](*url.URL){identity}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Minute),
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
}

func TestVerifierAcceptsAllowlistedServiceSPIFFEIdentity(t *testing.T) {
	now := time.Date(2026, 7, 17, 16, 0, 0, 0, time.UTC)
	verifier, err := NewVerifier("admin.platform.test", "agent-api, workspace-manager", "runtime:write runtime:read")
	if err != nil {
		t.Fatal(err)
	}
	verifier.Now = func() time.Time { return now }
	identity, err := verifier.VerifyMTLS(context.Background(), serviceCertificate(t, "spiffe://admin.platform.test/service/agent-api", now))
	if err != nil {
		t.Fatal(err)
	}
	if identity.SubjectID != "agent-api" || identity.TenantID != "" || len(identity.Scopes) != 2 || identity.Scopes[0] != "runtime:write" {
		t.Fatalf("identity=%+v", identity)
	}
}

func TestVerifierRejectsForeignMalformedAndServerCertificates(t *testing.T) {
	now := time.Date(2026, 7, 17, 16, 0, 0, 0, time.UTC)
	verifier, err := NewVerifier("admin.platform.test", "agent-api", "runtime:write")
	if err != nil {
		t.Fatal(err)
	}
	verifier.Now = func() time.Time { return now }
	for name, certificate := range map[string]*x509.Certificate{
		"foreign service": serviceCertificate(t, "spiffe://admin.platform.test/service/attacker", now),
		"foreign domain":  serviceCertificate(t, "spiffe://evil.test/service/agent-api", now),
		"nested path":     serviceCertificate(t, "spiffe://admin.platform.test/service/agent-api/extra", now),
		"expired":         serviceCertificate(t, "spiffe://admin.platform.test/service/agent-api", now.Add(-2*time.Minute)),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := verifier.VerifyMTLS(context.Background(), certificate); err == nil {
				t.Fatal("invalid service certificate accepted")
			}
		})
	}
	serverOnly := serviceCertificate(t, "spiffe://admin.platform.test/service/agent-api", now)
	serverOnly.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
	if _, err := verifier.VerifyMTLS(context.Background(), serverOnly); err == nil {
		t.Fatal("server-only certificate accepted as internal client")
	}
}

func TestVerifierConfigurationFailsClosed(t *testing.T) {
	for _, test := range []struct{ trust, services, scopes string }{
		{"", "agent-api", "runtime:read"},
		{"admin.platform.test", "", "runtime:read"},
		{"admin.platform.test", "Agent_API", "runtime:read"},
		{"admin.platform.test", "agent-api", ""},
	} {
		if _, err := NewVerifier(test.trust, test.services, test.scopes); err == nil {
			t.Fatalf("invalid config accepted: %+v", test)
		}
	}
}

func TestInternalServerAndClientEnforceTLSAndBindDelegatedTenant(t *testing.T) {
	now := time.Now().UTC()
	directory := t.TempDir()
	caCertificate, caKey := issueCA(t, now)
	caFile := writePEM(t, directory, "ca.pem", "CERTIFICATE", caCertificate.Raw)
	serverCertificate, serverKey := issueLeaf(t, caCertificate, caKey, now, true, "")
	serverCertificateFile := writePEM(t, directory, "server.pem", "CERTIFICATE", serverCertificate.Raw)
	serverKeyFile := writePrivateKey(t, directory, "server-key.pem", serverKey)
	clientCertificate, clientKey := issueLeaf(t, caCertificate, caKey, now, false, "spiffe://admin.platform.test/service/agent-api")
	clientCertificateFile := writePEM(t, directory, "client.pem", "CERTIFICATE", clientCertificate.Raw)
	clientKeyFile := writePrivateKey(t, directory, "client-key.pem", clientKey)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server, err := NewServer(ServerConfig{
		Address: listener.Addr().String(), CertificateFile: serverCertificateFile, PrivateKeyFile: serverKeyFile,
		ClientCAFile: caFile, TrustDomain: "admin.platform.test", AllowedServices: "agent-api", Scopes: "runtime:write",
	}, http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		identity, ok := httpauth.IdentityFromContext(request.Context())
		if !ok || identity.SubjectID != "agent-api" || identity.TenantID != "tenant-1" || identity.KindClaim != "SERVICE" || identity.Source != "mtls" {
			t.Errorf("identity=%+v ok=%t", identity, ok)
			http.Error(w, "bad identity", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	if err != nil {
		t.Fatal(err)
	}
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.ServeTLS(listener, "", "") }()
	t.Cleanup(func() {
		_ = server.Close()
		<-serveDone
	})

	client, err := NewClient(caFile, clientCertificateFile, clientKeyFile)
	if err != nil {
		t.Fatal(err)
	}
	request, _ := http.NewRequest(http.MethodGet, "https://"+listener.Addr().String()+"/internal", nil)
	request.Header.Set("X-Tenant-ID", "tenant-1")
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("status=%d", response.StatusCode)
	}

	roots := x509.NewCertPool()
	roots.AddCert(caCertificate)
	withoutCertificate := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: roots}}}
	if response, err := withoutCertificate.Get("https://" + listener.Addr().String() + "/internal"); err == nil {
		response.Body.Close()
		t.Fatal("client without service certificate reached internal API")
	}
}

func issueCA(t *testing.T, now time.Time) (*x509.Certificate, ed25519.PrivateKey) {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test internal CA"},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), IsCA: true,
		BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	raw, err := x509.CreateCertificate(rand.Reader, template, template, publicKey, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	certificate, err := x509.ParseCertificate(raw)
	if err != nil {
		t.Fatal(err)
	}
	return certificate, privateKey
}

func issueLeaf(t *testing.T, ca *x509.Certificate, caKey ed25519.PrivateKey, now time.Time, server bool, rawURI string) (*x509.Certificate, ed25519.PrivateKey) {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "internal leaf"},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature,
	}
	if server {
		template.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
		template.IPAddresses = []net.IP{net.ParseIP("127.0.0.1")}
	} else {
		template.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}
		identity, parseErr := url.Parse(rawURI)
		if parseErr != nil {
			t.Fatal(parseErr)
		}
		template.URIs = []*url.URL{identity}
	}
	raw, err := x509.CreateCertificate(rand.Reader, template, ca, publicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	certificate, err := x509.ParseCertificate(raw)
	if err != nil {
		t.Fatal(err)
	}
	return certificate, privateKey
}

func writePEM(t *testing.T, directory, name, blockType string, raw []byte) string {
	t.Helper()
	path := filepath.Join(directory, name)
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: blockType, Bytes: raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func writePrivateKey(t *testing.T, directory, name string, key ed25519.PrivateKey) string {
	t.Helper()
	raw, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return writePEM(t, directory, name, "PRIVATE KEY", raw)
}
