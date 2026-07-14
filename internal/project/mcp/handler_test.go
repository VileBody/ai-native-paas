package mcp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/keir-research/ai-native-paas/internal/agent/enrollment"
	attachmentsapp "github.com/keir-research/ai-native-paas/internal/attachments/application"
	attachmentsmemory "github.com/keir-research/ai-native-paas/internal/attachments/memory"
	attachmentstestkit "github.com/keir-research/ai-native-paas/internal/attachments/testkit"
	infraapp "github.com/keir-research/ai-native-paas/internal/infrastructure/application"
	inframemory "github.com/keir-research/ai-native-paas/internal/infrastructure/memory"
	sourceapp "github.com/keir-research/ai-native-paas/internal/source/application"
	"github.com/keir-research/ai-native-paas/internal/source/domain"
	"github.com/keir-research/ai-native-paas/internal/workspace"
	agentv2 "github.com/keir-research/ai-native-paas/pkg/contracts/agent/v2"
	attachmentsv1 "github.com/keir-research/ai-native-paas/pkg/contracts/attachments/v1"
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
	created      workspace.CreateRequest
	exec         workspace.ExecRequest
	scope        workspace.Scope
	revision     sourcev2.SourceRevision
	receipt      sourcev2.AgentCommitReceipt
	mergeRequest sourceapp.CreateMergeRequestCommand
}

type failingSecretCommands struct{ err error }

func (f failingSecretCommands) SetSecret(context.Context, attachmentsapp.SetSecretRequest) (attachmentsv1.SecretMetadata, attachmentsv1.AttachmentSnapshotRef, error) {
	return attachmentsv1.SecretMetadata{}, attachmentsv1.AttachmentSnapshotRef{}, f.err
}

func (f failingSecretCommands) ListProjectSecretMetadata(context.Context, string, string, string) ([]attachmentsv1.SecretMetadata, error) {
	return nil, f.err
}

func (w *workspaceCommands) CreateMergeRequest(_ context.Context, command sourceapp.CreateMergeRequestCommand) (sourceapp.CreateMergeRequestResult, error) {
	w.mergeRequest = command
	return sourceapp.CreateMergeRequestResult{
		RepositoryID: command.RepositoryID, ProviderIID: 7, SourceBranch: command.SourceBranch,
		TargetBranch: command.TargetBranch, HeadSHA: command.ExpectedHeadSHA, WebURL: "https://gitlab.com/beta/booking/-/merge_requests/7",
		SourcePlanHash: command.SourcePlanHash,
	}, nil
}

func (w *workspaceCommands) GetCommitReceipt(_ context.Context, _ workspace.Scope, _ string) (sourcev2.AgentCommitReceipt, error) {
	if w.receipt.Validate() != nil {
		return sourcev2.AgentCommitReceipt{}, workspace.ErrNotFound
	}
	return w.receipt, nil
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
	_ = repository.BeginProvisioning(clock.now)
	_ = repository.AttachProvider(42, "beta/booking", "https://gitlab.com/beta/booking", "main", clock.now)
	workspaces := &workspaceCommands{revision: sourcev2.SourceRevision{RepositoryID: repository.ID, CommitSHA: strings.Repeat("a", 40)}}
	infrastructure := &infraapp.Service{
		Store: inframemory.New(), Clock: clock, IDs: &mcpIDs{},
		Prices: infraapp.PriceBook{
			RateCard: commercev2.RateCard{RateCardID: "beta-test", Version: "1", MarkupBasisPoints: 2500, Currency: "RUB", PriceSnapshotID: "prices-test"},
			Prices:   map[string]infraapp.UnitPrice{"twc_server": {Meter: "server.month", Unit: "server-month", ProviderMinorPerQuantity: 1000, Known: true}},
		},
	}
	sourceChanges := &workspace.Service{Store: workspace.NewMemoryStore(), Clock: clock, IDs: &mcpIDs{}}
	return Handler{Enrollment: service, Projects: projectReader{project: project, repo: repository}, Workspaces: workspaces, Infrastructure: infrastructure, SourceChanges: sourceChanges, MergeRequests: workspaces}, access, workspaces
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
		PlanJSON:       json.RawMessage(`{"resource_changes":[{"address":"twc_server.app","provider_name":"timeweb","type":"twc_server","change":{"actions":["create"]}}]}`),
		RetainedResources: []infrastructurev1.RetainedResource{{
			Address: "cozystack_postgres.primary", Provider: "cozystack", ResourceType: "cozystack_postgres",
			ExternalID: "postgres-primary", Policy: "platform.yaml/v2:retain", Reason: "production data retention policy",
		}},
		CapturedAt: now,
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
	if err := json.Unmarshal(plannedEnvelope.Result, &plan); err != nil || !plan.Summary.RequiresApproval || len(plan.Summary.RetainedResources) != 1 || plan.Summary.RetainedResources[0].Address != "cozystack_postgres.primary" {
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

func TestAgent_SecretSetIsWriteOnlyAcrossMCPAndWorkspace(t *testing.T) {
	const sentinel = "G22_SECRET_SENTINEL_never_return_8fa29c"
	handler, access, workspaces := mcpFixture(t, []string{
		"agent.tool:secret_set", "agent.tool:secret_list_metadata", "agent.tool:workspace_exec",
	})
	store := attachmentsmemory.New()
	vault := attachmentstestkit.NewVault()
	logger := &attachmentstestkit.Logger{}
	attachments := &attachmentsapp.Service{
		Store: store,
		Environments: &attachmentstestkit.Environments{Values: map[string]attachmentsapp.EnvironmentRef{
			"env-1":       {TenantID: "tenant-1", ApplicationID: "project-1", EnvironmentID: "env-1", Name: "staging", Ready: true},
			"env-sibling": {TenantID: "tenant-1", ApplicationID: "project-2", EnvironmentID: "env-sibling", Name: "staging", Ready: true},
		}},
		Secrets: vault, Runtime: attachmentstestkit.NewRuntime(), Logger: logger,
		Clock: attachmentstestkit.NewClock(), IDs: &attachmentsapp.SequentialIDs{},
	}
	handler.Secrets = attachments

	set := invocation(agentv2.ToolSecretSet)
	set.IdempotencyKey = "secret-set-1"
	set.Arguments = json.RawMessage(`{"environment_id":"env-1","name":"API_TOKEN","value":"` + sentinel + `","scope":"runtime","phase":"runtime"}`)
	written := request(t, handler, http.MethodPost, "/projects/project-1/mcp/v2/invoke", access, set)
	assertSecretAbsent(t, written.Body.Bytes(), sentinel, "secret_set response")
	if written.Code != http.StatusOK || !vault.Contains(sentinel) {
		t.Fatalf("secret_set status=%d vault_contains=%t", written.Code, vault.Contains(sentinel))
	}
	var writtenEnvelope agentv2.InvocationResponse
	var writtenResult secretSetResult
	if json.Unmarshal(written.Body.Bytes(), &writtenEnvelope) != nil || json.Unmarshal(writtenEnvelope.Result, &writtenResult) != nil {
		t.Fatal("decode secret_set metadata response")
	}
	if writtenResult.Secret.Name != "API_TOKEN" || writtenResult.Secret.Version != 1 || writtenResult.Reference != "secret://project-1/env-1/API_TOKEN" || writtenResult.AttachmentSnapshot.SnapshotID == "" {
		t.Fatalf("unexpected secret metadata: %+v", writtenResult)
	}

	replayed := request(t, handler, http.MethodPost, "/projects/project-1/mcp/v2/invoke", access, set)
	assertSecretAbsent(t, replayed.Body.Bytes(), sentinel, "idempotent secret_set response")
	var replayedEnvelope agentv2.InvocationResponse
	var replayedResult secretSetResult
	if replayed.Code != http.StatusOK || json.Unmarshal(replayed.Body.Bytes(), &replayedEnvelope) != nil || json.Unmarshal(replayedEnvelope.Result, &replayedResult) != nil || replayedResult.Secret.Version != 1 || replayedResult.Reference != writtenResult.Reference {
		t.Fatal("idempotent secret_set replay changed public metadata")
	}

	list := invocation(agentv2.ToolSecretListMetadata)
	list.IdempotencyKey = "secret-list-1"
	list.Arguments = json.RawMessage(`{"environment_id":"env-1"}`)
	listed := request(t, handler, http.MethodPost, "/projects/project-1/mcp/v2/invoke", access, list)
	assertSecretAbsent(t, listed.Body.Bytes(), sentinel, "secret metadata list")
	var listedEnvelope agentv2.InvocationResponse
	var listedResult secretListResult
	if listed.Code != http.StatusOK || json.Unmarshal(listed.Body.Bytes(), &listedEnvelope) != nil || json.Unmarshal(listedEnvelope.Result, &listedResult) != nil || len(listedResult.Secrets) != 1 || listedResult.Secrets[0].Name != "API_TOKEN" {
		t.Fatal("secret metadata list did not return the written metadata")
	}

	sibling := list
	sibling.IdempotencyKey = "secret-list-sibling"
	sibling.Arguments = json.RawMessage(`{"environment_id":"env-sibling"}`)
	siblingResponse := request(t, handler, http.MethodPost, "/projects/project-1/mcp/v2/invoke", access, sibling)
	if siblingResponse.Code != http.StatusForbidden {
		t.Fatalf("sibling project environment list status=%d", siblingResponse.Code)
	}
	assertSecretAbsent(t, siblingResponse.Body.Bytes(), sentinel, "cross-project error")

	exec := invocation(agentv2.ToolWorkspaceExec)
	exec.IdempotencyKey = "workspace-with-secret-ref"
	exec.Arguments = json.RawMessage(`{"workspace_id":"workspace-1","argv":["python","-c","print('configured')"],"working_dir":"","environment_refs":{"API_TOKEN":"` + writtenResult.Reference + `"},"timeout_seconds":60,"output_limit_bytes":4096,"kind":"command"}`)
	executed := request(t, handler, http.MethodPost, "/projects/project-1/mcp/v2/invoke", access, exec)
	if executed.Code != http.StatusOK || workspaces.exec.Spec.EnvironmentRefs["API_TOKEN"] != writtenResult.Reference {
		t.Fatalf("workspace reference dispatch status=%d reference=%q", executed.Code, workspaces.exec.Spec.EnvironmentRefs["API_TOKEN"])
	}
	assertSecretAbsent(t, executed.Body.Bytes(), sentinel, "workspace response")
	assertSecretAbsent(t, mustJSON(t, workspaces.exec), sentinel, "workspace command")

	handler.Secrets = failingSecretCommands{err: fmt.Errorf("provider failure contained %s", sentinel)}
	failed := set
	failed.IdempotencyKey = "secret-set-failed"
	failedResponse := request(t, handler, http.MethodPost, "/projects/project-1/mcp/v2/invoke", access, failed)
	if failedResponse.Code != http.StatusServiceUnavailable {
		t.Fatalf("secret provider failure status=%d", failedResponse.Code)
	}
	assertSecretAbsent(t, failedResponse.Body.Bytes(), sentinel, "public provider error")
	assertSecretAbsent(t, mustJSON(t, store.Audit()), sentinel, "attachments audit")
	assertSecretAbsent(t, mustJSON(t, store.Outbox()), sentinel, "attachments outbox")
	if logger.Contains(sentinel) {
		t.Fatal("secret value leaked to structured logs")
	}
}

func assertSecretAbsent(t *testing.T, value []byte, secret, surface string) {
	t.Helper()
	if bytes.Contains(value, []byte(secret)) {
		t.Fatalf("secret value leaked through %s", surface)
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
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

func TestProjectMCP_GovernedRepositoryCommandsUseVerifiedBindings(t *testing.T) {
	handler, access, workspaces := mcpFixture(t, []string{
		"agent.tool:repository_create_branch", "agent.tool:repository_apply_patch", "agent.tool:repository_commit", "agent.tool:repository_push", "agent.tool:repository_create_merge_request",
	})
	create := invocation(agentv2.ToolRepositoryCreateBranch)
	create.IdempotencyKey = "source-checkout-1"
	create.Arguments = json.RawMessage(`{
		"workspace_id":"workspace-1","branch":"agent/task-1",
		"environment_refs":{"GIT_USERNAME":"credential://gitlab-project-1/username","GIT_TOKEN":"credential://gitlab-project-1/token"},
		"credential_leases":["gitlab-project-1"]
	}`)
	response := request(t, handler, http.MethodPost, "/projects/project-1/mcp/v2/invoke", access, create)
	if response.Code != http.StatusOK || workspaces.exec.Kind != "repository_checkout" || workspaces.exec.Spec.Argv[2] != "repo-1" || workspaces.exec.Spec.Argv[3] != "https://gitlab.com/beta/booking.git" || workspaces.exec.Spec.Argv[4] != strings.Repeat("a", 40) {
		t.Fatalf("checkout status=%d body=%s command=%#v", response.Code, response.Body.String(), workspaces.exec)
	}
	applyPatch := invocation(agentv2.ToolRepositoryApplyPatch)
	applyPatch.IdempotencyKey = "source-patch-1"
	applyPatch.Arguments = json.RawMessage(`{
		"workspace_id":"workspace-1","target_branch":"agent/task-1",
		"files":[{"path":"README.md","content_base64":"dXBkYXRlZAo="}]
	}`)
	response = request(t, handler, http.MethodPost, "/projects/project-1/mcp/v2/invoke", access, applyPatch)
	if response.Code != http.StatusOK || workspaces.exec.Kind != "repository_patch" {
		t.Fatalf("patch status=%d body=%s command=%#v", response.Code, response.Body.String(), workspaces.exec)
	}
	var patchEnvelope agentv2.InvocationResponse
	if err := json.Unmarshal(response.Body.Bytes(), &patchEnvelope); err != nil {
		t.Fatal(err)
	}
	var patchResult struct {
		Plan workspace.SourceChangePlan `json:"plan"`
	}
	if err := json.Unmarshal(patchEnvelope.Result, &patchResult); err != nil || patchResult.Plan.PlanID == "" {
		t.Fatalf("patch result=%#v err=%v", patchResult, err)
	}

	commit := invocation(agentv2.ToolRepositoryCommit)
	commit.IdempotencyKey = "source-commit-1"
	commit.Arguments, _ = json.Marshal(repositoryCommitArguments{WorkspaceID: "workspace-1", ChangePlanID: patchResult.Plan.PlanID, Branch: "agent/task-1", Message: "Implement governed change"})
	response = request(t, handler, http.MethodPost, "/projects/project-1/mcp/v2/invoke", access, commit)
	if response.Code != http.StatusOK || workspaces.exec.Kind != "repository_commit" || workspaces.exec.Spec.Argv[5] != "agent-1" || workspaces.exec.Spec.Argv[6] != "task-1" || workspaces.exec.Spec.Argv[7] != "corr-1" || workspaces.exec.Spec.Argv[8] != patchResult.Plan.PlanHash || !strings.Contains(response.Body.String(), "WAITING_DEPENDENCY") {
		t.Fatalf("commit status=%d body=%s command=%#v", response.Code, response.Body.String(), workspaces.exec)
	}

	push := invocation(agentv2.ToolRepositoryPush)
	push.IdempotencyKey = "source-push-1"
	push.Arguments = json.RawMessage(`{
		"workspace_id":"workspace-1","change_plan_id":"` + patchResult.Plan.PlanID + `","branch":"agent/task-1",
		"commit_sha":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","expected_remote_sha":"absent",
		"environment_refs":{"GIT_USERNAME":"credential://gitlab-project-1/username","GIT_TOKEN":"credential://gitlab-project-1/token"},
		"credential_leases":["gitlab-project-1"]
	}`)
	response = request(t, handler, http.MethodPost, "/projects/project-1/mcp/v2/invoke", access, push)
	if response.Code != http.StatusOK || workspaces.exec.Kind != "repository_push" || workspaces.exec.Spec.Argv[8] != "absent" || workspaces.exec.Spec.Argv[5] != strings.Repeat("a", 40) || workspaces.exec.Spec.Argv[7] != patchResult.Plan.PlanHash {
		t.Fatalf("push status=%d body=%s command=%#v", response.Code, response.Body.String(), workspaces.exec)
	}

	forgedStatement := sourcev2.CommitStatement{
		RepositoryID: "repo-1", BaseSHA: strings.Repeat("a", 40), CommitSHA: strings.Repeat("b", 40),
		Branch: "agent/task-1", AgentID: "agent-1", TaskID: "task-1", CorrelationID: "forged-correlation",
		SourcePlanHash: patchResult.Plan.PlanHash, IssuedAt: time.Date(2026, 7, 14, 5, 0, 1, 0, time.UTC),
	}
	workspaces.receipt = validMCPCommitReceipt(t, forgedStatement, "command-1")
	merge := invocation(agentv2.ToolRepositoryCreateMergeRequest)
	merge.IdempotencyKey = "source-mr-forged"
	merge.Arguments = json.RawMessage(`{
		"workspace_id":"workspace-1","change_plan_id":"` + patchResult.Plan.PlanID + `","commit_command_id":"command-1",
		"source_branch":"agent/task-1","title":"Implement governed change"
	}`)
	response = request(t, handler, http.MethodPost, "/projects/project-1/mcp/v2/invoke", access, merge)
	if response.Code != http.StatusForbidden || workspaces.mergeRequest.RepositoryID != "" {
		t.Fatalf("forged receipt status=%d body=%s command=%#v", response.Code, response.Body.String(), workspaces.mergeRequest)
	}
	forgedStatement.CorrelationID = "corr-1"
	workspaces.receipt = validMCPCommitReceipt(t, forgedStatement, "command-1")
	merge.IdempotencyKey = "source-mr-1"
	response = request(t, handler, http.MethodPost, "/projects/project-1/mcp/v2/invoke", access, merge)
	if response.Code != http.StatusOK || workspaces.mergeRequest.RepositoryID != "repo-1" || workspaces.mergeRequest.TargetBranch != "main" || workspaces.mergeRequest.ExpectedHeadSHA != strings.Repeat("b", 40) || workspaces.mergeRequest.SourcePlanHash != patchResult.Plan.PlanHash || workspaces.mergeRequest.ActorID != "agent-1" {
		t.Fatalf("merge request status=%d body=%s command=%#v", response.Code, response.Body.String(), workspaces.mergeRequest)
	}
}

func validMCPCommitReceipt(t *testing.T, statement sourcev2.CommitStatement, commandID string) sourcev2.AgentCommitReceipt {
	t.Helper()
	canonical, err := statement.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	statementDigest := sha256.Sum256(canonical)
	signature := []byte("test-signature")
	signatureDigest := sha256.Sum256(signature)
	return sourcev2.AgentCommitReceipt{
		SessionID: "session-1", ExecutionSessionID: "session-1", CommandID: commandID,
		Statement: statement,
		Attestation: sourcev2.CommitAttestation{
			RepositoryID: statement.RepositoryID, CommitSHA: statement.CommitSHA, AgentID: statement.AgentID,
			TaskID: statement.TaskID, CorrelationID: statement.CorrelationID,
			StatementDigest: "sha256:" + hex.EncodeToString(statementDigest[:]),
			SignatureDigest: "sha256:" + hex.EncodeToString(signatureDigest[:]), IssuedAt: statement.IssuedAt,
		},
		Signature: base64.StdEncoding.EncodeToString(signature), CertificateFingerprint: "sha256:" + strings.Repeat("c", 64),
	}
}

func TestProjectMCP_ProductionPatchFailsClosedWithoutExactSourceApproval(t *testing.T) {
	handler, access, workspaces := mcpFixture(t, []string{"agent.tool:repository_apply_patch"})
	patch := invocation(agentv2.ToolRepositoryApplyPatch)
	patch.Arguments = json.RawMessage(`{
		"workspace_id":"workspace-1","target_branch":"agent/task-1",
		"files":[{"path":"deploy/environments/production/deployment.yaml","content_base64":"YXBpVmVyc2lvbjogdjEK"}]
	}`)
	response := request(t, handler, http.MethodPost, "/projects/project-1/mcp/v2/invoke", access, patch)
	if response.Code != http.StatusOK || workspaces.exec.Kind != "" || !strings.Contains(response.Body.String(), "WAITING_APPROVAL") {
		t.Fatalf("production patch status=%d body=%s command=%#v", response.Code, response.Body.String(), workspaces.exec)
	}
	patch.ApprovalGrantID = "forged-grant"
	response = request(t, handler, http.MethodPost, "/projects/project-1/mcp/v2/invoke", access, patch)
	if response.Code != http.StatusForbidden || workspaces.exec.Kind != "" {
		t.Fatalf("forged approval status=%d body=%s command=%#v", response.Code, response.Body.String(), workspaces.exec)
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
