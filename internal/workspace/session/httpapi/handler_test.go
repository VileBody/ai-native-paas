package httpapi

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/keir-research/ai-native-paas/internal/workspace"
	"github.com/keir-research/ai-native-paas/internal/workspace/bootstrap"
	"github.com/keir-research/ai-native-paas/internal/workspace/session"
	workspacev1 "github.com/keir-research/ai-native-paas/pkg/contracts/workspace/v1"
)

type handlerClock struct{ now time.Time }

func (c handlerClock) Now() time.Time { return c.now }

type handlerIDs struct {
	mu   sync.Mutex
	next int
}

type bindingResolver struct{}

type certificateRotator struct{ identity bootstrap.Identity }

func (r *certificateRotator) Sign(_ context.Context, identity bootstrap.Identity, csr []byte) (bootstrap.CertificateBundle, error) {
	r.identity = identity
	if string(csr) != "test-csr" {
		return bootstrap.CertificateBundle{}, fmt.Errorf("unexpected CSR")
	}
	return bootstrap.CertificateBundle{Certificate: []byte("rotated-certificate"), CAChain: []byte("rotated-ca"), NotAfter: time.Date(2026, 7, 14, 12, 14, 0, 0, time.UTC)}, nil
}

func (bindingResolver) ResolveAgentBinding(_ context.Context, tenantID, projectID, workspaceID, taskID, correlationID string) (string, error) {
	if tenantID != "tenant-1" || projectID != "project-1" || workspaceID != "workspace-1" || taskID != "task-1" || correlationID != "correlation-1" {
		return "", workspace.ErrNotFound
	}
	return "vm-1", nil
}

func (i *handlerIDs) New(prefix string) string {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.next++
	return fmt.Sprintf("%s-%d", prefix, i.next)
}

func clientCertificate(t *testing.T, now time.Time, path string) *x509.Certificate {
	t.Helper()
	identity, err := url.Parse("spiffe://workspace.platform.example.com" + path)
	if err != nil {
		t.Fatal(err)
	}
	return &x509.Certificate{
		Raw: []byte("verified-cert:" + path), URIs: []*url.URL{identity},
		NotBefore: now.Add(-time.Minute), NotAfter: now.Add(10 * time.Minute), ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
}

func withCertificate(request *http.Request, certificate *x509.Certificate) *http.Request {
	request.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{certificate}, VerifiedChains: [][]*x509.Certificate{{certificate}}}
	return request
}

func handlerFixture(t *testing.T) (Handler, *session.Registry, handlerClock, *x509.Certificate) {
	t.Helper()
	clock := handlerClock{now: time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)}
	registry := &session.Registry{Store: session.NewMemoryStore(), Clock: clock, IDs: &handlerIDs{}, AckPollInterval: time.Millisecond, DispatchAckTimeout: time.Second}
	certificate := clientCertificate(t, clock.now, "/tenant/tenant-1/project/project-1/workspace/workspace-1/task/task-1/agent/agent-1")
	return Handler{Registry: registry, Bindings: bindingResolver{}, Certificates: &certificateRotator{}, Principals: SPIFFEResolver{TrustDomain: "workspace.platform.example.com"}, MaxBodyBytes: 4096, LongPoll: 5 * time.Millisecond}, registry, clock, certificate
}

func TestWorkspaceAgentHTTP_RotationUsesOnlyVerifiedSessionIdentity(t *testing.T) {
	handler, _, _, certificate := handlerFixture(t)
	connected := connectAgent(t, handler, certificate)
	rotator := &certificateRotator{}
	handler.Certificates = rotator
	body := fmt.Sprintf(`{"session_id":%q,"csr_pem":"test-csr"}`, connected.SessionID)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/workspace-agent/certificate:rotate", strings.NewReader(body))
	response := serve(handler, withCertificate(request, certificate))
	if response.Code != http.StatusOK {
		t.Fatalf("rotation status=%d body=%s", response.Code, response.Body.String())
	}
	if rotator.identity != (bootstrap.Identity{TenantID: "tenant-1", ProjectID: "project-1", WorkspaceID: "workspace-1", TaskID: "task-1", AgentID: "agent-1"}) {
		t.Fatalf("rotation identity=%#v", rotator.identity)
	}
	var view workspacev1.AgentCertificateView
	if err := json.Unmarshal(response.Body.Bytes(), &view); err != nil || view.CertificatePEM != "rotated-certificate" || view.CAChainPEM != "rotated-ca" {
		t.Fatalf("rotation response=%#v err=%v", view, err)
	}

	foreign := clientCertificate(t, time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC), "/tenant/tenant-1/project/project-2/workspace/workspace-1/task/task-1/agent/agent-1")
	foreignResponse := serve(handler, withCertificate(httptest.NewRequest(http.MethodPost, "/api/v1/workspace-agent/certificate:rotate", strings.NewReader(body)), foreign))
	if foreignResponse.Code != http.StatusForbidden {
		t.Fatalf("foreign session rotated certificate: %d", foreignResponse.Code)
	}
}

func serve(handler Handler, request *http.Request) *httptest.ResponseRecorder {
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func connectAgent(t *testing.T, handler Handler, certificate *x509.Certificate) workspacev1.AgentSessionView {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/workspace-agent/session", strings.NewReader(`{"vm_id":"attacker-supplied-vm","correlation_id":"correlation-1"}`))
	response := serve(handler, withCertificate(request, certificate))
	if response.Code != http.StatusCreated {
		t.Fatalf("connect status=%d body=%s", response.Code, response.Body.String())
	}
	var view workspacev1.AgentSessionView
	if err := json.Unmarshal(response.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	return view
}

func TestWorkspaceAgentHTTP_BindsVMFromPersistedCorrelationNotRequest(t *testing.T) {
	handler, registry, _, certificate := handlerFixture(t)
	connected := connectAgent(t, handler, certificate)
	stored, err := registry.Store.GetSession(context.Background(), connected.SessionID)
	if err != nil || stored.VMID != "vm-1" {
		t.Fatalf("session VM binding=%#v err=%v", stored, err)
	}

	request := httptest.NewRequest(http.MethodPost, "/api/v1/workspace-agent/session", strings.NewReader(`{"vm_id":"vm-1","correlation_id":"foreign-correlation"}`))
	response := serve(handler, withCertificate(request, certificate))
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("foreign correlation accepted: %d %s", response.Code, response.Body.String())
	}
}

func TestWorkspaceAgentHTTP_RequiresVerifiedMTLSAndIgnoresIdentityHeaders(t *testing.T) {
	handler, _, _, _ := handlerFixture(t)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/workspace-agent/session", strings.NewReader(`{"vm_id":"vm-1","tenant_id":"tenant-1"}`))
	request.Header.Set("X-Tenant-ID", "tenant-1")
	request.Header.Set("X-Project-ID", "project-1")
	response := serve(handler, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("development identity headers accepted without mTLS: %d %s", response.Code, response.Body.String())
	}
}

func TestWorkspaceAgentHTTP_OutboundPullAndAckAreCertificateBound(t *testing.T) {
	handler, registry, _, certificate := handlerFixture(t)
	connected := connectAgent(t, handler, certificate)
	envelope := workspace.CommandEnvelope{
		CommandID: "command-1", WorkspaceID: "workspace-1", ProjectID: "project-1", TaskID: "task-1",
		Spec: workspacev1.CommandSpec{Argv: []string{"tofu", "plan"}, WorkingDir: "repo", TimeoutSeconds: 60, OutputLimitBytes: 4096},
	}
	type dispatchResult struct {
		receipt workspace.DispatchReceipt
		err     error
	}
	done := make(chan dispatchResult, 1)
	go func() {
		receipt, err := registry.Dispatch(context.Background(), envelope)
		done <- dispatchResult{receipt: receipt, err: err}
	}()

	var message workspacev1.AgentMessage
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		request := httptest.NewRequest(http.MethodGet, "/api/v1/workspace-agent/messages:next?session_id="+url.QueryEscape(connected.SessionID), nil)
		response := serve(handler, withCertificate(request, certificate))
		if response.Code == http.StatusOK {
			if err := json.Unmarshal(response.Body.Bytes(), &message); err != nil {
				t.Fatal(err)
			}
			break
		}
		if response.Code != http.StatusNoContent {
			t.Fatalf("next status=%d body=%s", response.Code, response.Body.String())
		}
	}
	if message.MessageID == "" || message.CommandID != envelope.CommandID {
		t.Fatalf("outbound command missing: %#v", message)
	}
	ackBody := fmt.Sprintf(`{"session_id":%q,"accepted":true}`, connected.SessionID)
	ackRequest := httptest.NewRequest(http.MethodPost, "/api/v1/workspace-agent/messages/"+message.MessageID+":ack", strings.NewReader(ackBody))
	ackResponse := serve(handler, withCertificate(ackRequest, certificate))
	if ackResponse.Code != http.StatusNoContent {
		t.Fatalf("ack status=%d body=%s", ackResponse.Code, ackResponse.Body.String())
	}
	result := <-done
	if result.err != nil || !result.receipt.Accepted || result.receipt.AgentSessionID != connected.SessionID {
		t.Fatalf("dispatch receipt=%#v err=%v", result.receipt, result.err)
	}

	foreign := clientCertificate(t, time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC), "/tenant/tenant-1/project/project-2/workspace/workspace-1/task/task-1/agent/agent-1")
	foreignRequest := httptest.NewRequest(http.MethodPost, "/api/v1/workspace-agent/messages/"+message.MessageID+":ack", strings.NewReader(ackBody))
	foreignResponse := serve(handler, withCertificate(foreignRequest, foreign))
	if foreignResponse.Code != http.StatusForbidden {
		t.Fatalf("foreign certificate ack status=%d body=%s", foreignResponse.Code, foreignResponse.Body.String())
	}
}

func TestSPIFFEResolver_RejectsUnverifiedChainWrongTrustDomainAndNonClientCertificate(t *testing.T) {
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	resolver := SPIFFEResolver{TrustDomain: "workspace.platform.example.com"}
	valid := clientCertificate(t, now, "/tenant/tenant-1/project/project-1/workspace/workspace-1/task/task-1/agent/agent-1")
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{valid}}
	if _, err := resolver.Resolve(request); err == nil {
		t.Fatal("unverified client certificate accepted")
	}
	wrongDomain := clientCertificate(t, now, "/tenant/tenant-1/project/project-1/workspace/workspace-1/task/task-1/agent/agent-1")
	wrongDomain.URIs[0].Host = "attacker.example.com"
	if _, err := resolver.Resolve(withCertificate(httptest.NewRequest(http.MethodGet, "/", nil), wrongDomain)); err == nil {
		t.Fatal("foreign SPIFFE trust domain accepted")
	}
	serverOnly := clientCertificate(t, now, "/tenant/tenant-1/project/project-1/workspace/workspace-1/task/task-1/agent/agent-1")
	serverOnly.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
	if _, err := resolver.Resolve(withCertificate(httptest.NewRequest(http.MethodGet, "/", nil), serverOnly)); err == nil {
		t.Fatal("non-client certificate accepted")
	}
}
