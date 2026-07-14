package workspaceagent

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	infrastructurev1 "github.com/keir-research/ai-native-paas/pkg/contracts/infrastructure/v1"
	sourcev2 "github.com/keir-research/ai-native-paas/pkg/contracts/source/v2"
	workspacev1 "github.com/keir-research/ai-native-paas/pkg/contracts/workspace/v1"
)

const maximumAgentCertificateLifetime = 15 * time.Minute

type Client struct {
	base            *url.URL
	http            *http.Client
	workspaceID     string
	correlationID   string
	certificateFile string
	privateKeyFile  string
	caFile          string
	mu              sync.RWMutex
	certificate     tls.Certificate
	identityURI     string
	gateway         *url.URL
	roots           *x509.CertPool
}

func NewClient(config Config) (*Client, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	base, _ := url.Parse(strings.TrimRight(config.ControlPlaneURL, "/"))
	pair, identityURI, err := loadCertificateSet(config.CertificateFile, config.PrivateKeyFile, config.CAFile, time.Now().UTC())
	if err != nil {
		pair, identityURI, err = loadCertificateSet(config.CertificateFile+".previous", config.PrivateKeyFile+".previous", config.CAFile+".previous", time.Now().UTC())
		if err != nil {
			return nil, errors.New("load workspace agent mTLS identity")
		}
	}
	if workspaceFromSPIFFE(identityURI) != config.WorkspaceID {
		return nil, errors.New("workspace agent mTLS identity does not match configuration")
	}
	client := &Client{
		base: base, workspaceID: config.WorkspaceID, correlationID: config.CorrelationID,
		certificateFile: config.CertificateFile, privateKeyFile: config.PrivateKeyFile, caFile: config.CAFile,
		certificate: pair, identityURI: identityURI,
	}
	gateway, _ := url.Parse(strings.TrimRight(config.EgressGatewayURL, "/"))
	client.gateway = gateway
	caRaw, err := readSecureFile(config.CAFile, 256<<10)
	if err != nil {
		caRaw, err = readSecureFile(config.CAFile+".previous", 256<<10)
	}
	if err != nil {
		return nil, errors.New("load workspace control-plane CA")
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(caRaw) {
		return nil, errors.New("parse workspace control-plane CA")
	}
	client.roots = roots
	transport := &http.Transport{
		Proxy:             nil,
		DialContext:       client.DialEgress,
		ForceAttemptHTTP2: true,
		TLSClientConfig: &tls.Config{
			MinVersion: tls.VersionTLS13, RootCAs: roots,
			GetClientCertificate: func(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
				client.mu.RLock()
				defer client.mu.RUnlock()
				copy := client.certificate
				return &copy, nil
			},
		},
		TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 25 * time.Second,
		IdleConnTimeout: 60 * time.Second, MaxIdleConns: 4, MaxIdleConnsPerHost: 4,
	}
	client.http = &http.Client{
		Transport: transport,
		Timeout:   30 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return errors.New("workspace control-plane redirect rejected")
		},
	}
	return client, nil
}

func (c *Client) Connect(ctx context.Context) (workspacev1.AgentSessionView, error) {
	var view workspacev1.AgentSessionView
	err := c.doJSON(ctx, http.MethodPost, "/api/v1/workspace-agent/session", workspacev1.AgentSessionConnect{CorrelationID: c.correlationID}, http.StatusCreated, &view)
	if err != nil || strings.TrimSpace(view.SessionID) == "" || view.WorkspaceID != c.workspaceID || !view.ExpiresAt.After(time.Now().UTC()) {
		return workspacev1.AgentSessionView{}, errors.New("workspace agent session connect failed")
	}
	return view, nil
}

func (c *Client) Heartbeat(ctx context.Context, sessionID string) error {
	return c.doJSON(ctx, http.MethodPost, "/api/v1/workspace-agent/session:heartbeat", workspacev1.AgentHeartbeat{SessionID: sessionID}, http.StatusNoContent, nil)
}

func (c *Client) Next(ctx context.Context, sessionID string) (*workspacev1.AgentMessage, error) {
	endpoint := "/api/v1/workspace-agent/messages:next?session_id=" + url.QueryEscape(sessionID)
	var message workspacev1.AgentMessage
	status, err := c.request(ctx, http.MethodGet, endpoint, nil, &message)
	if err != nil {
		return nil, err
	}
	if status == http.StatusNoContent {
		return nil, nil
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("workspace control-plane request failed with status %d", status)
	}
	return &message, nil
}

func (c *Client) Acknowledge(ctx context.Context, sessionID, messageID string, accepted bool) error {
	if !identityPattern.MatchString(messageID) {
		return errors.New("workspace agent message identity is invalid")
	}
	endpoint := "/api/v1/workspace-agent/messages/" + url.PathEscape(messageID) + ":ack"
	return c.doJSON(ctx, http.MethodPost, endpoint, workspacev1.AgentMessageAck{SessionID: sessionID, Accepted: accepted}, http.StatusNoContent, nil)
}

func (c *Client) Outcome(ctx context.Context, outcome workspacev1.AgentCommandOutcome) error {
	return c.doJSON(ctx, http.MethodPost, "/api/v1/workspace-agent/outcomes", outcome, http.StatusOK, nil)
}

func (c *Client) ResolveEnvironment(ctx context.Context, request workspacev1.AgentCredentialResolve) (workspacev1.AgentCredentialView, error) {
	var view workspacev1.AgentCredentialView
	if err := c.doJSON(ctx, http.MethodPost, "/api/v1/workspace-agent/credentials:resolve", request, http.StatusOK, &view); err != nil {
		return workspacev1.AgentCredentialView{}, err
	}
	if len(view.Values) > 128 || !view.ExpiresAt.After(time.Now().UTC().Add(15*time.Second)) {
		return workspacev1.AgentCredentialView{}, errors.New("workspace credential response is invalid")
	}
	for name, value := range view.Values {
		if !environmentVariableName(name) || reservedEnvironmentName(name) || value == "" || len(value) > 64<<10 || strings.ContainsRune(value, '\x00') {
			return workspacev1.AgentCredentialView{}, errors.New("workspace credential response is invalid")
		}
	}
	return view, nil
}

func (c *Client) UploadOutput(ctx context.Context, chunk workspacev1.AgentOutputChunk) error {
	if chunk.Validate() != nil {
		return errors.New("workspace output chunk is invalid")
	}
	return c.doJSON(ctx, http.MethodPost, "/api/v1/workspace-agent/output-chunks", chunk, http.StatusNoContent, nil)
}

func (c *Client) SubmitPlanReceipt(ctx context.Context, receipt infrastructurev1.AgentPlanReceipt) error {
	if receipt.Validate() != nil {
		return errors.New("workspace plan receipt is invalid")
	}
	return c.doJSON(ctx, http.MethodPost, "/api/v1/workspace-agent/plan-receipts", receipt, http.StatusNoContent, nil)
}

func (c *Client) SubmitCommitReceipt(ctx context.Context, receipt sourcev2.AgentCommitReceipt) error {
	if receipt.Validate() != nil {
		return errors.New("workspace commit receipt is invalid")
	}
	return c.doJSON(ctx, http.MethodPost, "/api/v1/workspace-agent/commit-receipts", receipt, http.StatusNoContent, nil)
}

func (c *Client) Rotate(ctx context.Context, sessionID string) error {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return errors.New("generate workspace agent rotation key")
	}
	csrDER, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{}, key)
	if err != nil {
		return errors.New("create workspace agent rotation CSR")
	}
	csrPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csrDER})
	var view workspacev1.AgentCertificateView
	if err := c.doJSON(ctx, http.MethodPost, "/api/v1/workspace-agent/certificate:rotate", workspacev1.AgentCertificateRotate{SessionID: sessionID, CSRPEM: string(csrPEM)}, http.StatusOK, &view); err != nil {
		return err
	}
	privateDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return errors.New("encode workspace agent rotation key")
	}
	privatePEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateDER})
	pair, identityURI, err := parseCertificateSet([]byte(view.CertificatePEM), privatePEM, []byte(view.CAChainPEM), time.Now().UTC())
	if err != nil {
		return errors.New("validate rotated workspace agent identity")
	}
	c.mu.RLock()
	expectedIdentity := c.identityURI
	c.mu.RUnlock()
	if identityURI != expectedIdentity || !pair.Leaf.NotAfter.Equal(view.NotAfter.UTC()) {
		return errors.New("rotated workspace agent identity changed")
	}
	if err := replaceCredentialSet(c.certificateFile, c.privateKeyFile, c.caFile, []byte(view.CertificatePEM), privatePEM, []byte(view.CAChainPEM)); err != nil {
		return errors.New("persist rotated workspace agent identity")
	}
	c.mu.Lock()
	c.certificate = pair
	c.identityURI = identityURI
	c.mu.Unlock()
	if transport, ok := c.http.Transport.(*http.Transport); ok {
		transport.CloseIdleConnections()
	}
	return nil
}

func (c *Client) CertificateNotAfter() time.Time {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.certificate.Leaf == nil {
		return time.Time{}
	}
	return c.certificate.Leaf.NotAfter.UTC()
}

func (c *Client) doJSON(ctx context.Context, method, endpoint string, input any, wanted int, output any) error {
	status, err := c.request(ctx, method, endpoint, input, output)
	if err != nil {
		return err
	}
	if status != wanted {
		return fmt.Errorf("workspace control-plane request failed with status %d", status)
	}
	return nil
}

func (c *Client) request(ctx context.Context, method, endpoint string, input, output any) (int, error) {
	if c == nil || c.base == nil || c.http == nil || !strings.HasPrefix(endpoint, "/api/v1/workspace-agent/") {
		return 0, errors.New("workspace control-plane client is unavailable")
	}
	var body io.Reader
	if input != nil {
		raw, err := json.Marshal(input)
		if err != nil {
			return 0, errors.New("encode workspace control-plane request")
		}
		body = bytes.NewReader(raw)
	}
	target := *c.base
	parts := strings.SplitN(endpoint, "?", 2)
	target.Path = parts[0]
	if len(parts) == 2 {
		target.RawQuery = parts[1]
	}
	request, err := http.NewRequestWithContext(ctx, method, target.String(), body)
	if err != nil {
		return 0, errors.New("create workspace control-plane request")
	}
	request.Header.Set("Accept", "application/json")
	if input != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := c.http.Do(request)
	if err != nil {
		return 0, errors.New("workspace control-plane request failed")
	}
	defer response.Body.Close()
	if output == nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
		return response.StatusCode, nil
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	if err != nil || len(raw) > 1<<20 {
		return 0, errors.New("workspace control-plane response is invalid")
	}
	if response.StatusCode >= 200 && response.StatusCode < 300 && response.StatusCode != http.StatusNoContent {
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(output); err != nil {
			return 0, errors.New("workspace control-plane response is invalid")
		}
	}
	return response.StatusCode, nil
}

func loadCertificateSet(certificateFile, privateKeyFile, caFile string, now time.Time) (tls.Certificate, string, error) {
	certificatePEM, err := readSecureFile(certificateFile, 256<<10)
	if err != nil {
		return tls.Certificate{}, "", err
	}
	privatePEM, err := readSecureFile(privateKeyFile, 64<<10)
	if err != nil {
		return tls.Certificate{}, "", err
	}
	caPEM, err := readSecureFile(caFile, 256<<10)
	if err != nil {
		return tls.Certificate{}, "", err
	}
	return parseCertificateSet(certificatePEM, privatePEM, caPEM, now)
}

func parseCertificateSet(certificatePEM, privatePEM, caPEM []byte, now time.Time) (tls.Certificate, string, error) {
	pair, err := tls.X509KeyPair(append(append([]byte(nil), certificatePEM...), caPEM...), privatePEM)
	if err != nil || len(pair.Certificate) < 1 {
		return tls.Certificate{}, "", errors.New("workspace agent key pair is invalid")
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil || len(leaf.URIs) != 1 || leaf.URIs[0].Scheme != "spiffe" || leaf.URIs[0].Host == "" || leaf.URIs[0].User != nil || leaf.URIs[0].RawQuery != "" || leaf.URIs[0].Fragment != "" || workspaceFromSPIFFE(leaf.URIs[0].String()) == "" || !hasClientOnlyUsage(leaf) {
		return tls.Certificate{}, "", errors.New("workspace agent certificate identity is invalid")
	}
	if !leaf.NotAfter.After(now.Add(time.Minute)) || leaf.NotAfter.Sub(leaf.NotBefore) > maximumAgentCertificateLifetime+2*time.Minute {
		return tls.Certificate{}, "", errors.New("workspace agent certificate lifetime is invalid")
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(caPEM) {
		return tls.Certificate{}, "", errors.New("workspace agent CA is invalid")
	}
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: roots, CurrentTime: now, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
		return tls.Certificate{}, "", errors.New("workspace agent certificate chain is invalid")
	}
	pair.Leaf = leaf
	return pair, leaf.URIs[0].String(), nil
}

func workspaceFromSPIFFE(raw string) string {
	identity, err := url.Parse(raw)
	if err != nil || identity.Scheme != "spiffe" || identity.Host == "" {
		return ""
	}
	segments := strings.Split(strings.Trim(identity.EscapedPath(), "/"), "/")
	if len(segments) != 10 || segments[0] != "tenant" || segments[2] != "project" || segments[4] != "workspace" || segments[6] != "task" || segments[8] != "agent" {
		return ""
	}
	for _, index := range []int{1, 3, 5, 7, 9} {
		value, err := url.PathUnescape(segments[index])
		if err != nil || !identityPattern.MatchString(value) {
			return ""
		}
		segments[index] = value
	}
	return segments[5]
}

func hasClientOnlyUsage(certificate *x509.Certificate) bool {
	client := false
	for _, usage := range certificate.ExtKeyUsage {
		if usage == x509.ExtKeyUsageServerAuth || usage == x509.ExtKeyUsageAny {
			return false
		}
		if usage == x509.ExtKeyUsageClientAuth {
			client = true
		}
	}
	return client
}

func replaceCredentialSet(certificateFile, privateKeyFile, caFile string, certificatePEM, privatePEM, caPEM []byte) error {
	for _, item := range []struct {
		path string
		raw  []byte
	}{{certificateFile, certificatePEM}, {privateKeyFile, privatePEM}, {caFile, caPEM}} {
		old, err := readSecureFile(item.path, 256<<10)
		if err != nil || writeAtomic(item.path+".previous", old, 0o400) != nil {
			return errors.New("preserve previous workspace identity")
		}
	}
	for _, item := range []struct {
		path string
		raw  []byte
	}{{privateKeyFile, privatePEM}, {certificateFile, certificatePEM}, {caFile, caPEM}} {
		if err := writeAtomic(item.path, item.raw, 0o400); err != nil {
			return err
		}
	}
	return syncDirectory(filepath.Dir(certificateFile))
}

func writeAtomic(filename string, raw []byte, mode os.FileMode) error {
	directory := filepath.Dir(filename)
	file, err := os.CreateTemp(directory, ".workspace-agent-credential-*")
	if err != nil {
		return err
	}
	temporary := file.Name()
	defer os.Remove(temporary)
	if err := file.Chmod(mode); err != nil {
		_ = file.Close()
		return err
	}
	if _, err := file.Write(raw); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(temporary, filename)
}

func syncDirectory(directory string) error {
	file, err := os.Open(directory)
	if err != nil {
		return err
	}
	defer file.Close()
	return file.Sync()
}
