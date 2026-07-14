package workspaceagent

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestClient_DialsOnlyThroughAuthenticatedEgressGateway(t *testing.T) {
	now := time.Now().UTC()
	caCertificate, caKey := tunnelCA(t, now)
	identity, _ := url.Parse("spiffe://workspace.platform.example.com/tenant/tenant-1/project/project-1/workspace/workspace-1/task/task-1/agent/agent-1")
	clientCertificate, clientKey := tunnelLeaf(t, now, caCertificate, caKey, nil, []*url.URL{identity}, []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth})
	serverCertificate, serverKey := tunnelLeaf(t, now, caCertificate, caKey, []net.IP{net.ParseIP("127.0.0.1")}, nil, []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth})
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caCertificate.Raw})
	serverPair, err := tls.X509KeyPair(serverCertificate, serverKey)
	if err != nil {
		t.Fatal(err)
	}
	clientCAs := x509.NewCertPool()
	clientCAs.AppendCertsFromPEM(caPEM)
	listener, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{
		MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{serverPair}, ClientCAs: clientCAs,
		ClientAuth: tls.RequireAndVerifyClientCert, NextProtos: []string{"http/1.1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	serverResult := make(chan error, 1)
	go func() {
		connection, acceptErr := listener.Accept()
		if acceptErr != nil {
			serverResult <- acceptErr
			return
		}
		defer connection.Close()
		request, readErr := http.ReadRequest(bufio.NewReader(connection))
		if readErr != nil {
			serverResult <- readErr
			return
		}
		if request.Method != http.MethodConnect || request.Host != "api.example.com:443" {
			serverResult <- io.ErrUnexpectedEOF
			return
		}
		if _, writeErr := io.WriteString(connection, "HTTP/1.1 200 Connection Established\r\n\r\n"); writeErr != nil {
			serverResult <- writeErr
			return
		}
		buffer := make([]byte, 4)
		if _, readErr := io.ReadFull(connection, buffer); readErr != nil {
			serverResult <- readErr
			return
		}
		_, writeErr := connection.Write(buffer)
		serverResult <- writeErr
	}()
	directory := t.TempDir()
	certificateFile := secureTestFile(t, directory, "agent.crt", clientCertificate)
	privateKeyFile := secureTestFile(t, directory, "agent.key", clientKey)
	caFile := secureTestFile(t, directory, "ca.crt", caPEM)
	client, err := NewClient(Config{
		ControlPlaneURL: "https://control.example.com", EgressGatewayURL: "https://" + listener.Addr().String(),
		WorkspaceID: "workspace-1", CorrelationID: "correlation-1", CertificateFile: certificateFile, PrivateKeyFile: privateKeyFile, CAFile: caFile,
		JournalDirectory: "/state/journal", WorkspaceRoot: "/workspace",
	})
	if err != nil {
		t.Fatal(err)
	}
	connection, err := client.DialEgress(context.Background(), "tcp", "api.example.com:443")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := connection.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 4)
	if _, err := io.ReadFull(connection, buffer); err != nil || string(buffer) != "ping" {
		t.Fatalf("tunnel echo=%q err=%v", buffer, err)
	}
	_ = connection.Close()
	if err := <-serverResult; err != nil {
		t.Fatal(err)
	}
	if _, err := client.DialEgress(context.Background(), "tcp", "api.example.com:80"); err == nil {
		t.Fatal("non-HTTPS destination accepted")
	}
}

func tunnelCA(t *testing.T, now time.Time) (*x509.Certificate, *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(100), Subject: pkix.Name{CommonName: "workspace-test-ca"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	certificate, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return certificate, key
}

func tunnelLeaf(t *testing.T, now time.Time, ca *x509.Certificate, caKey *ecdsa.PrivateKey, addresses []net.IP, uris []*url.URL, usages []x509.ExtKeyUsage) ([]byte, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(now.UnixNano()), Subject: pkix.Name{CommonName: "workspace-test-leaf"}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(10 * time.Minute),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: usages, IPAddresses: addresses, URIs: uris,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, ca, &key.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	privateDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateDER})
}

func secureTestFile(t *testing.T, directory, name string, content []byte) string {
	t.Helper()
	filename := filepath.Join(directory, name)
	if err := os.WriteFile(filename, content, 0o600); err != nil {
		t.Fatal(err)
	}
	return filename
}
