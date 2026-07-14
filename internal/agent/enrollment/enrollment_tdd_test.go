package enrollment

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
)

type clock struct{ now time.Time }

func (c *clock) Now() time.Time { return c.now }

type ids struct {
	mu   sync.Mutex
	next int
}

func (i *ids) New(prefix string) string {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.next++
	return fmt.Sprintf("%s-%d", prefix, i.next)
}

type secrets struct {
	mu   sync.Mutex
	next int
}

func (s *secrets) Secret(int) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.next++
	return fmt.Sprintf("opaque-secret-with-at-least-thirty-two-bytes-%d", s.next), nil
}

func testService() (*Service, *clock) {
	c := &clock{now: time.Date(2026, time.July, 14, 12, 0, 0, 0, time.UTC)}
	return &Service{Store: NewMemoryStore(), Clock: c, IDs: &ids{}, Secrets: &secrets{}, Signer: HMACSigner{Key: []byte("0123456789abcdef0123456789abcdef")}}, c
}

type certificateIssuer struct {
	now     time.Time
	subject WorkspaceCertificateSubject
	ttl     time.Duration
}

func (i *certificateIssuer) IssueWorkspaceCertificate(_ context.Context, subject WorkspaceCertificateSubject, _ string, ttl time.Duration) (WorkspaceCertificate, error) {
	i.subject = subject
	i.ttl = ttl
	return WorkspaceCertificate{CertificateID: "certificate-1", CertificatePEM: "certificate", CAChainPEM: "ca-chain", ExpiresAt: i.now.Add(ttl)}, nil
}

func TestAgent_ProjectMCPTokenIsBoundToAgentUserTenantAndProject(t *testing.T) {
	service, _ := testService()
	binding := Binding{TenantID: "tenant-1", ProjectID: "project-1", UserID: "user-1", AgentID: "agent-1", Scopes: []string{"agent.tool:project_get"}}
	enrollment, err := service.Issue(context.Background(), binding)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Exchange(context.Background(), enrollment.Token, "agent-2", "ssh-ed25519 wrong"); err == nil {
		t.Fatal("enrollment token used by another agent")
	}
	refresh, err := service.Exchange(context.Background(), enrollment.Token, "agent-1", "ssh-ed25519 correct")
	if err != nil {
		t.Fatalf("bound enrollment exchange failed: %v", err)
	}
	if _, err := service.Exchange(context.Background(), enrollment.Token, "agent-1", "ssh-ed25519 correct"); err == nil {
		t.Fatal("one-time enrollment token was reused")
	}
	access, claims, err := service.Access(context.Background(), refresh.RefreshToken)
	if err != nil {
		t.Fatal(err)
	}
	if claims.ExpiresAt-claims.IssuedAt != int64(AccessTTL/time.Second) {
		t.Fatalf("access JWT lifetime=%d", claims.ExpiresAt-claims.IssuedAt)
	}
	if _, err := service.VerifyAccess(access, binding); err != nil {
		t.Fatalf("bound access token rejected: %v", err)
	}
	if claims, err := service.AuthenticateAccess(access, binding.ProjectID); err != nil || claims.AgentID != binding.AgentID {
		t.Fatalf("project route rejected signed access token: claims=%#v err=%v", claims, err)
	}
	if _, err := service.AuthenticateAccess(access, "project-2"); err == nil {
		t.Fatal("access token crossed project route binding")
	}
	mutations := []Binding{
		{TenantID: "tenant-2", ProjectID: "project-1", UserID: "user-1", AgentID: "agent-1", Scopes: binding.Scopes},
		{TenantID: "tenant-1", ProjectID: "project-2", UserID: "user-1", AgentID: "agent-1", Scopes: binding.Scopes},
		{TenantID: "tenant-1", ProjectID: "project-1", UserID: "user-2", AgentID: "agent-1", Scopes: binding.Scopes},
		{TenantID: "tenant-1", ProjectID: "project-1", UserID: "user-1", AgentID: "agent-2", Scopes: binding.Scopes},
	}
	for _, mutation := range mutations {
		if _, err := service.VerifyAccess(access, mutation); err == nil {
			t.Fatalf("token accepted for mismatched binding: %#v", mutation)
		}
	}
}

func TestWorkspaceCertificateIsShortLivedAndProjectBound(t *testing.T) {
	service, clock := testService()
	issuer := &certificateIssuer{now: clock.Now()}
	service.WorkspaceCertificates = issuer
	binding := Binding{TenantID: "tenant-1", ProjectID: "project-1", UserID: "user-1", AgentID: "agent-1", Scopes: []string{"agent.tool:workspace_create"}}
	enrollment, err := service.Issue(context.Background(), binding)
	if err != nil {
		t.Fatal(err)
	}
	refresh, err := service.Exchange(context.Background(), enrollment.Token, binding.AgentID, "ssh-ed25519 key")
	if err != nil {
		t.Fatal(err)
	}
	access, _, err := service.Access(context.Background(), refresh.RefreshToken)
	if err != nil {
		t.Fatal(err)
	}
	certificate, err := service.IssueWorkspaceCertificate(context.Background(), access, binding, "workspace-1", "task-1", "-----BEGIN CERTIFICATE REQUEST-----\ncsr\n-----END CERTIFICATE REQUEST-----")
	if err != nil {
		t.Fatal(err)
	}
	if issuer.ttl != AccessTTL || certificate.ExpiresAt.Sub(clock.Now()) != AccessTTL || issuer.subject.ProjectID != binding.ProjectID || issuer.subject.WorkspaceID != "workspace-1" {
		t.Fatalf("workspace certificate is not short-lived or scoped: issuer=%#v cert=%#v", issuer, certificate)
	}
}
