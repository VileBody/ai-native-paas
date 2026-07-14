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
	infraapp "github.com/keir-research/ai-native-paas/internal/infrastructure/application"
	inframemory "github.com/keir-research/ai-native-paas/internal/infrastructure/memory"
	"github.com/keir-research/ai-native-paas/internal/source/domain"
	"github.com/keir-research/ai-native-paas/internal/workspace"
	agentv2 "github.com/keir-research/ai-native-paas/pkg/contracts/agent/v2"
	commercev2 "github.com/keir-research/ai-native-paas/pkg/contracts/commerce/v2"
	infrastructurev1 "github.com/keir-research/ai-native-paas/pkg/contracts/infrastructure/v1"
	sourcev2 "github.com/keir-research/ai-native-paas/pkg/contracts/source/v2"
	workspacev1 "github.com/keir-research/ai-native-paas/pkg/contracts/workspace/v1"
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

type workspaceCommands struct {
	created  workspace.CreateRequest
	exec     workspace.ExecRequest
	scope    workspace.Scope
	revision sourcev2.SourceRevision
}

func (w *workspaceCommands) Create(_ context.Context, request workspace.CreateRequest) (workspacev1.WorkspaceRef, error) {
	w.created = request
	return workspacev1.WorkspaceRef{WorkspaceID: "workspace-1", ProjectID: request.Scope.ProjectID, TaskID: request.Spec.TaskID, State: workspacev1.WorkspaceProvisioning}, nil
}
func (w *workspaceCommands) Get(_ context.Context, scope workspace.Scope, workspaceID string) (workspacev1.WorkspaceRef, error) {
	w.scope = scope
	return workspacev1.WorkspaceRef{WorkspaceID: workspaceID, ProjectID: scope.ProjectID, TaskID: "task-1", State: workspacev1.WorkspaceReady}, nil
}
func (w *workspaceCommands) GetSourceRevision(_ context.Context, scope workspace.Scope, _ string) (sourcev2.SourceRevision, error) {
	w.scope = scope
	return w.revision, nil
}
func (w *workspaceCommands) Exec(_ context.Context, request workspace.ExecRequest) (workspacev1.CommandView, error) {
	w.exec = request
	return workspacev1.CommandView{CommandID: "command-1", WorkspaceID: request.WorkspaceID, State: workspacev1.CommandQueued}, nil
}
func (w *workspaceCommands) Destroy(_ context.Context, scope workspace.Scope, workspaceID string) (workspacev1.WorkspaceRef, error) {
	w.scope = scope
	return workspacev1.WorkspaceRef{WorkspaceID: workspaceID, ProjectID: scope.ProjectID, TaskID: "task-1", State: workspacev1.WorkspaceDestroying}, nil
}

func mcpFixture(t *testing.T, scopes []string) (Handler, string, *workspaceCommands) {
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
	workspaces := &workspaceCommands{revision: sourcev2.SourceRevision{RepositoryID: repository.ID, CommitSHA: strings.Repeat("a", 40)}}
	infrastructure := &infraapp.Service{
		Store: inframemory.New(), Clock: clock, IDs: &mcpIDs{},
		Prices: infraapp.PriceBook{
			RateCard: commercev2.RateCard{RateCardID: "beta-test", Version: "1", MarkupBasisPoints: 2500, Currency: "RUB", PriceSnapshotID: "prices-test"},
			Prices:   map[string]infraapp.UnitPrice{"twc_server": {Meter: "server.month", Unit: "server-month", ProviderMinorPerQuantity: 1000, Known: true}},
		},
	}
	return Handler{Enrollment: service, Projects: projectReader{project: project, repo: repository}, Workspaces: workspaces, Infrastructure: infrastructure}, access, workspaces
}

func TestProjectMCP_ExactPlanApprovalGatesVerifiedWorkspaceApply(t *testing.T) {
	scopes := []string{
		"agent.tool:workspace_exec", "agent.tool:infra_plan", "agent.tool:infra_get_plan",
		"agent.tool:infra_apply", "agent.tool:approval_request", "agent.tool:approval_get",
	}
	handler, access, workspaces := mcpFixture(t, scopes)

	bypass := invocation(agentv2.ToolWorkspaceExec)
	bypass.Arguments = json.RawMessage(`{"workspace_id":"workspace-1","argv":["tofu","apply","saved.plan"],"working_dir":"infrastructure","timeout_seconds":60,"output_limit_bytes":4096,"kind":"command"}`)
	response := request(t, handler, http.MethodPost, "/projects/project-1/mcp/v2/invoke", access, bypass)
	if response.Code != http.StatusBadRequest || workspaces.exec.WorkspaceID != "" {
		t.Fatalf("direct apply bypass status=%d body=%s request=%#v", response.Code, response.Body.String(), workspaces.exec)
	}

	planRequest := invocation(agentv2.ToolInfraPlan)
	planRequest.IdempotencyKey = "plan-1"
	planRequest.Arguments = json.RawMessage(`{
		"workspace_id":"workspace-1","target":"production",
		"source_sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"state_generation":1,
		"plan_path":"saved.plan","working_dir":"infrastructure"
	}`)
	waitingPlan := request(t, handler, http.MethodPost, "/projects/project-1/mcp/v2/invoke", access, planRequest)
	if waitingPlan.Code != http.StatusOK || !strings.Contains(waitingPlan.Body.String(), "WAITING_DEPENDENCY") || workspaces.exec.Kind != "infra_plan" || workspaces.exec.Spec.Argv[0] != "workspace-agent" || workspaces.exec.Spec.Argv[3] != strings.Repeat("a", 40) {
		t.Fatalf("waiting plan status=%d body=%s request=%#v", waitingPlan.Code, waitingPlan.Body.String(), workspaces.exec)
	}
	infrastructure := handler.Infrastructure.(*infraapp.Service)
	receipts := infrastructure.Store.(*inframemory.Store)
	now := time.Date(2026, 7, 14, 5, 0, 0, 0, time.UTC)
	if err := receipts.PutPlanReceipt(context.Background(), workspace.PlanReceiptScope{
		TenantID: "tenant-1", ProjectID: "project-1", WorkspaceID: "workspace-1", TaskID: "task-1", CommandID: "command-1", ActorID: "agent-1",
	}, infrastructurev1.AgentPlanReceipt{
		SessionID: "session-1", ExecutionSessionID: "session-1", CommandID: "command-1",
		ArtifactDigest: "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		PlanJSON:       json.RawMessage(`{"resource_changes":[{"address":"twc_server.app","provider_name":"timeweb","type":"twc_server","change":{"actions":["create"]}}]}`), CapturedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	planned := request(t, handler, http.MethodPost, "/projects/project-1/mcp/v2/invoke", access, planRequest)
	if planned.Code != http.StatusOK {
		t.Fatalf("plan status=%d body=%s", planned.Code, planned.Body.String())
	}
	var plannedEnvelope agentv2.InvocationResponse
	if err := json.Unmarshal(planned.Body.Bytes(), &plannedEnvelope); err != nil {
		t.Fatal(err)
	}
	var plan infraapp.PlanResult
	if err := json.Unmarshal(plannedEnvelope.Result, &plan); err != nil || !plan.Summary.RequiresApproval {
		t.Fatalf("plan=%#v err=%v", plan, err)
	}

	waitingRequest := invocation(agentv2.ToolApprovalRequest)
	waitingRequest.Arguments, _ = json.Marshal(infraPlanLookupArguments{PlanID: plan.Summary.PlanID})
	waiting := request(t, handler, http.MethodPost, "/projects/project-1/mcp/v2/invoke", access, waitingRequest)
	if waiting.Code != http.StatusOK || !strings.Contains(waiting.Body.String(), "WAITING_APPROVAL") {
		t.Fatalf("approval request status=%d body=%s", waiting.Code, waiting.Body.String())
	}

	grant, err := infrastructure.GrantApproval(context.Background(), infraapp.GrantApprovalCommand{
		TenantID: "tenant-1", ProjectID: "project-1", PlanID: plan.Summary.PlanID,
		ApproverUserID: "user-1", ExpiresAt: mcpClock{now: time.Date(2026, 7, 14, 5, 0, 0, 0, time.UTC)}.now.Add(5 * time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}

	apply := invocation(agentv2.ToolInfraApply)
	apply.IdempotencyKey = "apply-1"
	apply.ApprovalGrantID = grant.GrantID
	apply.Arguments, _ = json.Marshal(infraApplyArguments{
		PlanID: plan.Summary.PlanID, PlanHash: plan.Summary.PlanHash, EstimateVersion: plan.Estimate.Version,
		ReservationID: plan.Reservation.ReservationID, Target: "production", PlanPath: "saved.plan",
		WorkingDir: "infrastructure", TimeoutSeconds: 300, OutputLimitBytes: 4096,
	})
	applied := request(t, handler, http.MethodPost, "/projects/project-1/mcp/v2/invoke", access, apply)
	if applied.Code != http.StatusOK || workspaces.exec.Kind != "infra_apply" || workspaces.exec.Spec.Argv[0] != "workspace-agent" || workspaces.exec.Spec.Argv[2] != "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" {
		t.Fatalf("apply status=%d body=%s request=%#v", applied.Code, applied.Body.String(), workspaces.exec)
	}
}

func TestAgent_DeployWorkflowStartsFromExactRepositoryRevision(t *testing.T) {
	handler, access, workspaces := mcpFixture(t, []string{"agent.tool:infra_plan"})
	requestPlan := invocation(agentv2.ToolInfraPlan)
	requestPlan.IdempotencyKey = "plan-spoofed-revision"
	requestPlan.Arguments = json.RawMessage(`{
		"workspace_id":"workspace-1","target":"staging",
		"source_sha":"ffffffffffffffffffffffffffffffffffffffff",
		"state_generation":1,"plan_path":"saved.plan","working_dir":"infrastructure"
	}`)
	response := request(t, handler, http.MethodPost, "/projects/project-1/mcp/v2/invoke", access, requestPlan)
	if response.Code != http.StatusForbidden || workspaces.exec.WorkspaceID != "" {
		t.Fatalf("unbound revision plan status=%d body=%s command=%#v", response.Code, response.Body.String(), workspaces.exec)
	}
}

func TestProjectMCP_GenericExecCannotBypassGovernedGitMutation(t *testing.T) {
	handler, access, workspaces := mcpFixture(t, []string{"agent.tool:workspace_exec"})
	for _, argv := range []json.RawMessage{
		json.RawMessage(`["git","commit","-am","bypass"]`),
		json.RawMessage(`["git","push","origin","HEAD"]`),
		json.RawMessage(`["git","-c","credential.helper=evil","status"]`),
	} {
		invocation := invocation(agentv2.ToolWorkspaceExec)
		invocation.Arguments = json.RawMessage(`{"workspace_id":"workspace-1","argv":` + string(argv) + `,"working_dir":"","timeout_seconds":60,"output_limit_bytes":4096,"kind":"command"}`)
		response := request(t, handler, http.MethodPost, "/projects/project-1/mcp/v2/invoke", access, invocation)
		if response.Code != http.StatusBadRequest || workspaces.exec.WorkspaceID != "" {
			t.Fatalf("governed git bypass status=%d body=%s command=%#v", response.Code, response.Body.String(), workspaces.exec)
		}
	}
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
	handler, access, _ := mcpFixture(t, []string{"agent.tool:project_get", "agent.tool:repository_status"})
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
	handler, access, _ := mcpFixture(t, []string{"agent.tool:project_get", "agent.tool:workspace_get"})
	denied := request(t, handler, http.MethodPost, "/projects/project-1/mcp/v2/invoke", access, invocation(agentv2.ToolRepositoryStatus))
	if denied.Code != http.StatusForbidden {
		t.Fatalf("scope status=%d body=%s", denied.Code, denied.Body.String())
	}
	invalid := request(t, handler, http.MethodPost, "/projects/project-1/mcp/v2/invoke", access, invocation(agentv2.ToolWorkspaceGet))
	if invalid.Code != http.StatusBadRequest || !strings.Contains(invalid.Body.String(), "INVALID_ARGUMENT") {
		t.Fatalf("invalid workspace arguments status=%d body=%s", invalid.Code, invalid.Body.String())
	}
}

func TestProjectMCP_WorkspaceToolsDeriveScopeAndProjectFromAccessCredential(t *testing.T) {
	scopes := []string{"agent.tool:workspace_create", "agent.tool:workspace_get", "agent.tool:workspace_exec", "agent.tool:workspace_destroy"}
	handler, access, workspaces := mcpFixture(t, scopes)
	create := invocation(agentv2.ToolWorkspaceCreate)
	create.Arguments = json.RawMessage(`{"repository_id":"repo-1","commit_sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","image_digest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","cpu_millis":2000,"memory_mib":4096,"ttl_seconds":900,"network_profile":"isolated-governed"}`)
	created := request(t, handler, http.MethodPost, "/projects/project-1/mcp/v2/invoke", access, create)
	if created.Code != http.StatusOK || workspaces.created.Scope.TenantID != "tenant-1" || workspaces.created.Scope.ProjectID != "project-1" || workspaces.created.Scope.ActorID != "agent-1" || workspaces.created.Spec.ProjectID != "project-1" || workspaces.created.Spec.TaskID != "task-1" || workspaces.created.Spec.SourceRevision == nil || workspaces.created.Spec.SourceRevision.RepositoryID != "repo-1" {
		t.Fatalf("created status=%d body=%s request=%#v", created.Code, created.Body.String(), workspaces.created)
	}

	exec := invocation(agentv2.ToolWorkspaceExec)
	exec.IdempotencyKey = "exec-1"
	exec.Arguments = json.RawMessage(`{"workspace_id":"workspace-1","argv":["tofu","plan"],"working_dir":"infrastructure","timeout_seconds":60,"output_limit_bytes":4096,"kind":"infra_plan","serialization_key":"staging"}`)
	executed := request(t, handler, http.MethodPost, "/projects/project-1/mcp/v2/invoke", access, exec)
	if executed.Code != http.StatusOK || workspaces.exec.Scope.ProjectID != "project-1" || workspaces.exec.WorkspaceID != "workspace-1" || workspaces.exec.Spec.Argv[0] != "tofu" {
		t.Fatalf("exec status=%d body=%s request=%#v", executed.Code, executed.Body.String(), workspaces.exec)
	}

	// Scope-like arguments are not merely ignored: the closed schema rejects
	// them before the workspace service can observe a request.
	before := workspaces.created
	crossScope := invocation(agentv2.ToolWorkspaceCreate)
	crossScope.IdempotencyKey = "create-cross"
	crossScope.Arguments = json.RawMessage(`{"project_id":"victim","repository_id":"repo-1","commit_sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","image_digest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","cpu_millis":2000,"memory_mib":4096,"ttl_seconds":900,"network_profile":"isolated-governed"}`)
	rejected := request(t, handler, http.MethodPost, "/projects/project-1/mcp/v2/invoke", access, crossScope)
	if rejected.Code != http.StatusBadRequest || workspaces.created.IdempotencyKey != before.IdempotencyKey {
		t.Fatalf("scope override status=%d body=%s request=%#v", rejected.Code, rejected.Body.String(), workspaces.created)
	}
}
