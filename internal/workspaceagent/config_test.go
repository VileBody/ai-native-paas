package workspaceagent

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWorkspaceAgentConfig_RequiresClosedSecureProductionInput(t *testing.T) {
	directory := t.TempDir()
	filename := filepath.Join(directory, "agent.json")
	config := Config{
		ControlPlaneURL: "https://workspace.example.com", EgressGatewayURL: "https://egress.example.com:8443", WorkspaceID: "workspace-1", CorrelationID: "correlation-1",
		CertificateFile: "/identity/agent.crt", PrivateKeyFile: "/identity/agent.key", CAFile: "/identity/ca.crt",
		JournalDirectory: "/state/journal", WorkspaceRoot: "/workspace",
	}
	raw, _ := json.Marshal(config)
	if err := os.WriteFile(filename, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadConfig(filename)
	if err != nil || loaded != config {
		t.Fatalf("loaded=%#v err=%v", loaded, err)
	}
	if err := os.WriteFile(filename, append(raw[:len(raw)-1], []byte(`,"unknown":true}`)...), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(filename); err == nil {
		t.Fatal("unknown workspace agent configuration field accepted")
	}
	if err := os.WriteFile(filename, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filename, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(filename); err == nil {
		t.Fatal("world-readable workspace agent configuration accepted")
	}
	config.ControlPlaneURL = "https://workspace.example.com/untrusted-prefix"
	if err := config.Validate(); err == nil {
		t.Fatal("control-plane URL path prefix accepted")
	}
	config.ControlPlaneURL = "https://workspace.example.com"
	config.EgressGatewayURL = "https://egress.example.com"
	if err := config.Validate(); err == nil {
		t.Fatal("egress gateway without an explicit governed port accepted")
	}
}

func TestWorkspaceAgentClient_ValidatesExactClientOnlySPIFFEKeyPair(t *testing.T) {
	now := time.Date(2026, 7, 14, 18, 0, 0, 0, time.UTC)
	identity, _ := url.Parse("spiffe://workspace.platform.example.com/tenant/tenant-1/project/project-1/workspace/workspace-1/task/task-1/agent/agent-1")
	certificate, key, ca := agentCertificate(t, now, identity, []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth})
	pair, rawIdentity, err := parseCertificateSet(certificate, key, ca, now)
	if err != nil || pair.Leaf == nil || rawIdentity != identity.String() || workspaceFromSPIFFE(rawIdentity) != "workspace-1" {
		t.Fatalf("identity=%q pair=%#v err=%v", rawIdentity, pair.Leaf, err)
	}
	wrong, _ := url.Parse("spiffe://workspace.platform.example.com/workspace/workspace-1")
	certificate, key, ca = agentCertificate(t, now, wrong, []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth})
	if _, _, err := parseCertificateSet(certificate, key, ca, now); err == nil {
		t.Fatal("malformed SPIFFE path accepted")
	}
	certificate, key, ca = agentCertificate(t, now, identity, []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth})
	if _, _, err := parseCertificateSet(certificate, key, ca, now); err == nil {
		t.Fatal("server-auth workspace certificate accepted")
	}
}

func agentCertificate(t *testing.T, now time.Time, identity *url.URL, usages []x509.ExtKeyUsage) ([]byte, []byte, []byte) {
	t.Helper()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	caTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "agent-ca"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	leafTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(2), URIs: []*url.URL{identity}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(10 * time.Minute),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: usages,
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTemplate, caTemplate, &key.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	privateDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateDER}), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})
}
