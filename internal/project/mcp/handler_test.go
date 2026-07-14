package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/keir-research/ai-native-paas/internal/agent/enrollment"
	"github.com/keir-research/ai-native-paas/internal/source/domain"
	agentv2 "github.com/keir-research/ai-native-paas/pkg/contracts/agent/v2"
)

type mcpClock struct{ now time.Time }

func (c mcpClock) Now() time.Time { return c.now }

type mcpIDs struct {
	mu sync.Mutex
	n  int
}

func (i *mcpIDs) New(prefix string) string {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.n++
	return fmt.Sprintf("%s-%d", prefix, i.n)
}

type projectReader struct {
	project domain.Project
	repo    domain.Repository
}

func (r projectReader) GetProject(_ context.Context, tenantID, projectID string) (domain.Project, error) {
	if tenantID != r.project.TenantID || projectID != r.project.ID {
		return domain.Project{}, domain.NewError(domain.CodeNotFound, "project not found")
	}
	return r.project, nil
}
func (r projectReader) GetRepositoryForProject(_ context.Context, tenantID, projectID string) (domain.Repository, error) {
	if tenantID != r.repo.TenantID || projectID != r.repo.ProjectID {
		return domain.Repository{}, domain.NewError(domain.CodeNotFound, "repository not found")
	}
	return r.repo, nil
}

func mcpFixture(t *testing.T, scopes []string) (Handler, string) {
	t.Helper()
	clock := mcpClock{now: time.Date(2026, 7, 14, 5, 0, 0, 0, time.UTC)}
	service := &enrollment.Service{
		Store: enrollment.NewMemoryStore(), Clock: clock, IDs: &mcpIDs{}, Secrets: enrollment.CryptoSecrets{},
		Signer: enrollment.HMACSigner{Key: []byte("0123456789abcdef0123456789abcdef")},
	}
	binding := enrollment.Binding{TenantID: "tenant-1", ProjectID: "project-1", UserID: "user-1", AgentID: "agent-1", Scopes: scopes}
	issued, err := service.Issue(context.Background(), binding)
	if err != nil {
		t.Fatal(err)
	}
	refresh, err := service.Exchange(context.Background(), issued.Token, binding.AgentID, "ssh-ed25519 public")
	if err != nil {
		t.Fatal(err)
	}
	access, _, err := service.Access(context.Background(), refresh.RefreshToken)
	if err != nil {
		t.Fatal(err)
	}
	project, _ := domain.NewProject("project-1", "tenant-1", "booking", clock.now)
	repository, _ := domain.NewRepository("repo-1", "tenant-1", project.ID, "gitlab", "corr-1", 77, clock.now)
	return Handler{Enrollment: service, Projects: projectReader{project: project, repo: repository}}, access
}

func request(t *testing.T, handler http.Handler, method, path, token string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var reader *bytes.Reader
	if body == nil {
		reader = bytes.NewReader(nil)
	} else {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(raw)
	}
	httpRequest := httptest.NewRequest(method, path, reader)
	if token != "" {
		httpRequest.Header.Set("Authorization", "Bearer "+token)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httpRequest)
	return response
}

func invocation(tool agentv2.Tool) agentv2.InvocationRequest {
	return agentv2.InvocationRequest{
		APIVersion: agentv2.APIVersion, SemanticsVersion: agentv2.SemanticsVersion,
		TaskID: "task-1", Tool: tool, Arguments: json.RawMessage(`{"tenant_id":"victim"}`),
		IdempotencyKey: "read-1", CorrelationID: "corr-1",
	}
}

func TestProjectMCP_ToolCatalogAndReadsRequireProjectBoundAccess(t *testing.T) {
	handler, access := mcpFixture(t, []string{"agent.tool:project_get", "agent.tool:repository_status"})
	tools := request(t, handler, http.MethodGet, "/projects/project-1/mcp/v2/tools", access, nil)
	if tools.Code != http.StatusOK || !strings.Contains(tools.Body.String(), string(agentv2.ToolWorkspaceCreate)) {
		t.Fatalf("tools status=%d body=%s", tools.Code, tools.Body.String())
	}
	project := request(t, handler, http.MethodPost, "/projects/project-1/mcp/v2/invoke", access, invocation(agentv2.ToolProjectGet))
	if project.Code != http.StatusOK || !strings.Contains(project.Body.String(), "project-1") || strings.Contains(project.Body.String(), "victim") {
		t.Fatalf("project status=%d body=%s", project.Code, project.Body.String())
	}
	crossProject := request(t, handler, http.MethodGet, "/projects/project-2/mcp/v2/tools", access, nil)
	if crossProject.Code != http.StatusUnauthorized {
		t.Fatalf("cross-project status=%d", crossProject.Code)
	}
}

func TestProjectMCP_EnforcesToolScopeAndReportsUnavailableAdapters(t *testing.T) {
	handler, access := mcpFixture(t, []string{"agent.tool:project_get", "agent.tool:workspace_get"})
	denied := request(t, handler, http.MethodPost, "/projects/project-1/mcp/v2/invoke", access, invocation(agentv2.ToolRepositoryStatus))
	if denied.Code != http.StatusForbidden {
		t.Fatalf("scope status=%d body=%s", denied.Code, denied.Body.String())
	}
	unimplemented := request(t, handler, http.MethodPost, "/projects/project-1/mcp/v2/invoke", access, invocation(agentv2.ToolWorkspaceGet))
	if unimplemented.Code != http.StatusNotImplemented || !strings.Contains(unimplemented.Body.String(), "NOT_IMPLEMENTED") {
		t.Fatalf("unimplemented status=%d body=%s", unimplemented.Code, unimplemented.Body.String())
	}
}
