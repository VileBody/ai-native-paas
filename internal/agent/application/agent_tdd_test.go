package application_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	kernelv1 "github.com/keir-research/ai-native-paas/contracts/kernel/v1"
	"github.com/keir-research/ai-native-paas/internal/agent/application"
	"github.com/keir-research/ai-native-paas/internal/agent/domain"
	"github.com/keir-research/ai-native-paas/internal/agent/memory"
	"github.com/keir-research/ai-native-paas/internal/agent/testkit"
	agentv1 "github.com/keir-research/ai-native-paas/pkg/contracts/agent/v1"
	buildv1 "github.com/keir-research/ai-native-paas/pkg/contracts/build/v1"
	commercev1 "github.com/keir-research/ai-native-paas/pkg/contracts/commerce/v1"
	runtimev1 "github.com/keir-research/ai-native-paas/pkg/contracts/runtime/v1"
	"strings"
	"sync"
	"testing"
	"time"
)

var baseTime = time.Date(2026, 7, 13, 9, 0, 0, 0, time.UTC)

type fixture struct {
	svc                       *application.Service
	store                     *memory.Store
	clock                     *testkit.Clock
	ids                       *testkit.IDs
	source                    *testkit.Source
	builds                    *testkit.Builds
	runtime                   *testkit.Runtime
	attachments               *testkit.Attachments
	commerce                  *testkit.Commerce
	ops                       *testkit.Operations
	logs                      *testkit.Logs
	usage                     *testkit.Usage
	tenant, agent, user, task string
}

func newFixture(t *testing.T, policy agentv1.BudgetPolicy, scopes ...string) *fixture {
	t.Helper()
	if policy.RepairThreshold == 0 {
		policy = agentv1.BudgetPolicy{MaxBuildCount: 10, MaxBuildMinutes: 100, MaxDeployCount: 10, RepairThreshold: 3}
	}
	if len(scopes) == 0 {
		for _, tool := range agentv1.ToolCatalog() {
			scopes = append(scopes, string(agentv1.ScopeForTool(tool)))
		}
	}
	f := &fixture{store: memory.New(), clock: &testkit.Clock{T: baseTime}, ids: &testkit.IDs{}, source: testkit.NewSource(), builds: testkit.NewBuilds(), runtime: testkit.NewRuntime(), attachments: testkit.NewAttachments(), commerce: &testkit.Commerce{Allowed: true}, ops: testkit.NewOperations(), logs: &testkit.Logs{Lines: []string{"ready", "token=topsecret"}}, usage: &testkit.Usage{Preview: commercev1.InvoicePreview{TenantID: "tenant-1", PeriodID: "period-1", Currency: "EUR"}}, tenant: "tenant-1", agent: "agent-1", user: "user-1", task: "task-1"}
	f.svc = &application.Service{Store: f.store, Clock: f.clock, IDs: f.ids, Source: f.source, Builds: f.builds, Runtime: f.runtime, Attachments: f.attachments, Commerce: f.commerce, Operations: f.ops, Logs: f.logs, Usage: f.usage}
	if _, err := f.svc.RegisterPrincipal(context.Background(), application.RegisterPrincipalCommand{ID: f.agent, TenantID: f.tenant, OnBehalfOfUserID: f.user, Scopes: scopes, CredentialExpiresAt: baseTime.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.StartTask(context.Background(), application.StartTaskCommand{ID: f.task, TenantID: f.tenant, AgentID: f.agent, OnBehalfOfUserID: f.user, CorrelationID: "corr-1", BudgetPolicy: policy}); err != nil {
		t.Fatal(err)
	}
	return f
}
func (f *fixture) request(tool agentv1.Tool, args any, key string) agentv1.InvocationRequest {
	b, _ := json.Marshal(args)
	return agentv1.InvocationRequest{APIVersion: agentv1.APIVersion, SemanticsVersion: agentv1.SemanticsVersion, TenantID: f.tenant, AgentID: f.agent, TaskID: f.task, Tool: tool, Arguments: b, IdempotencyKey: key, CorrelationID: "corr-1"}
}
func invokeOK(t *testing.T, f *fixture, req agentv1.InvocationRequest) agentv1.InvocationResponse {
	t.Helper()
	r, err := f.svc.Invoke(context.Background(), req)
	if err != nil {
		t.Fatalf("invoke %s: %v response=%+v", req.Tool, err, r)
	}
	return r
}
func buildArgs(minutes int64) application.RequestBuildArguments {
	var a application.RequestBuildArguments
	a.Revision.ProjectID = "project-1"
	a.Revision.RepositoryID = "repo-project-1"
	a.Revision.Branch = "main"
	a.Revision.CommitSHA = "abcdef0123456789"
	a.EstimatedMinutes = minutes
	return a
}
func deployArgs(env string, rev int64) application.DeployArguments {
	return application.DeployArguments{BuildID: "build-1", ApplicationID: "app-1", EnvironmentID: "env-" + env, EnvironmentName: env, ExpectedEnvironmentRevision: rev, Configuration: runtimev1.ReleaseConfig{Region: "eu1", Isolation: runtimev1.IsolationSandboxed, Unit: "u1", Processes: map[string]runtimev1.ProcessSpec{"web": {Port: 8080, MinReplicas: 1, MaxReplicas: 2, HealthPath: "/health"}}, GeneratedHostname: "app.example.test", RolloutTimeoutSeconds: 300, EgressProfile: "public-default"}}
}
func seedBuild(f *fixture) {
	a := buildv1.ArtifactRef{ArtifactID: "artifact-build-1", Repository: "registry.invalid/tenant-1/app", Digest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", MediaType: "application/vnd.oci.image.manifest.v1+json"}
	f.builds.ByID["build-1"] = buildv1.BuildView{BuildID: "build-1", TenantID: f.tenant, State: buildv1.BuildSucceeded, Artifact: &a}
}
func approvalFor(t *testing.T, f *fixture, args application.DeployArguments) string {
	t.Helper()
	payload, _ := json.Marshal(args)
	r := invokeOK(t, f, f.request(agentv1.ToolRequestApproval, application.RequestApprovalArguments{Action: agentv1.ApprovalDeployProduction, Resource: agentv1.ApprovalResource{Type: "environment", ID: args.EnvironmentID}, Payload: payload, TTLSeconds: 600}, "approval-key"))
	var view agentv1.ApprovalRequestView
	if err := json.Unmarshal(r.Result.Data, &view); err != nil {
		t.Fatal(err)
	}
	g, err := f.svc.GrantApprovalForTenant(context.Background(), f.tenant, view.ApprovalRequestID, "approver-1", "user")
	if err != nil {
		t.Fatal(err)
	}
	return g.ID
}
func errCode(err error) domain.Code {
	var d *domain.Error
	if errors.As(err, &d) {
		return d.Code
	}
	return ""
}

func TestAgentPrincipal_IsDistinctFromUserPrincipal(t *testing.T) {
	f := newFixture(t, agentv1.BudgetPolicy{})
	if f.agent == f.user {
		t.Fatal("agent and user must differ")
	}
}
func TestAgentPrincipal_RecordsOnBehalfOfUser(t *testing.T) {
	f := newFixture(t, agentv1.BudgetPolicy{})
	v, err := f.svc.GetTask(context.Background(), f.tenant, f.task)
	if err != nil || v.OnBehalfOfUserID != f.user {
		t.Fatalf("view=%+v err=%v", v, err)
	}
}
func TestAgentScope_AllowsExplicitTool(t *testing.T) {
	f := newFixture(t, agentv1.BudgetPolicy{}, string(agentv1.ScopeForTool(agentv1.ToolCreateProject)))
	invokeOK(t, f, f.request(agentv1.ToolCreateProject, application.CreateProjectArguments{Name: "booking"}, "k1"))
}
func TestAgentScope_DeniesUnlistedTool(t *testing.T) {
	f := newFixture(t, agentv1.BudgetPolicy{}, string(agentv1.ScopeForTool(agentv1.ToolGetProject)))
	_, err := f.svc.Invoke(context.Background(), f.request(agentv1.ToolCreateProject, application.CreateProjectArguments{Name: "booking"}, "k1"))
	if errCode(err) != domain.CodePermissionDenied {
		t.Fatalf("err=%v", err)
	}
}
func TestAgentScope_CannotEscalateViaToolArguments(t *testing.T) {
	f := newFixture(t, agentv1.BudgetPolicy{}, string(agentv1.ScopeForTool(agentv1.ToolGetProject)))
	raw := json.RawMessage(`{"name":"booking","scope":"*"}`)
	req := f.request(agentv1.ToolCreateProject, map[string]any{}, "k1")
	req.Arguments = raw
	_, err := f.svc.Invoke(context.Background(), req)
	if err == nil {
		t.Fatal("expected denial")
	}
}
func TestAgentScope_CrossTenantResourceDenied(t *testing.T) {
	f := newFixture(t, agentv1.BudgetPolicy{})
	req := f.request(agentv1.ToolCreateProject, application.CreateProjectArguments{Name: "booking"}, "k1")
	req.TenantID = "tenant-2"
	_, err := f.svc.Invoke(context.Background(), req)
	if errCode(err) != domain.CodePermissionDenied {
		t.Fatalf("err=%v", err)
	}
}

func TestMCPToolSchemas_AreVersionedAndStable(t *testing.T) {
	if agentv1.APIVersion != "agent.platform.example.com/v1" || agentv1.SemanticsVersion != "v1" || len(agentv1.ToolCatalog()) != 20 {
		t.Fatal("contract drift")
	}
}
func TestMCPToolSchemas_RejectUnknownRequiredSemantics(t *testing.T) {
	f := newFixture(t, agentv1.BudgetPolicy{})
	r := f.request(agentv1.ToolCreateProject, application.CreateProjectArguments{Name: "x"}, "k")
	r.SemanticsVersion = "v2"
	if _, err := f.svc.Invoke(context.Background(), r); err == nil {
		t.Fatal("expected rejection")
	}
}
func TestMCPToolSchemas_ValidateIdentifiersAndLimits(t *testing.T) {
	f := newFixture(t, agentv1.BudgetPolicy{})
	_, err := f.svc.Invoke(context.Background(), f.request(agentv1.ToolGetProject, application.GetProjectArguments{ProjectID: "bad/id"}, "k"))
	if err == nil {
		t.Fatal("expected validation")
	}
}
func TestMCPResponse_AlwaysIncludesOperationOrResultReference(t *testing.T) {
	f := newFixture(t, agentv1.BudgetPolicy{})
	r := invokeOK(t, f, f.request(agentv1.ToolCreateProject, application.CreateProjectArguments{Name: "x"}, "k"))
	if err := r.Validate(); err != nil {
		t.Fatal(err)
	}
}
func TestMCPError_UsesStablePublicErrorContract(t *testing.T) {
	f := newFixture(t, agentv1.BudgetPolicy{}, string(agentv1.ScopeForTool(agentv1.ToolGetProject)))
	r, err := f.svc.Invoke(context.Background(), f.request(agentv1.ToolCreateProject, application.CreateProjectArguments{Name: "x"}, "k"))
	if err == nil || r.Error == nil || r.Error.Code != "PERMISSION_DENIED" || r.Error.Message == "" {
		t.Fatalf("response=%+v err=%v", r, err)
	}
}

func TestAgentResponse_NeverContainsGitLabAdminToken(t *testing.T) {
	f := newFixture(t, agentv1.BudgetPolicy{})
	f.source.Credential = "gitlab-admin-supersecret"
	r := invokeOK(t, f, f.request(agentv1.ToolCreateProject, application.CreateProjectArguments{Name: "x"}, "k"))
	b, _ := json.Marshal(r)
	if strings.Contains(string(b), f.source.Credential) {
		t.Fatal("credential leaked")
	}
}
func TestAgentResponse_NeverContainsKubernetesCredential(t *testing.T) {
	f := newFixture(t, agentv1.BudgetPolicy{})
	seedBuild(f)
	r := invokeOK(t, f, f.request(agentv1.ToolDeploy, deployArgs("staging", 1), "k"))
	b, _ := json.Marshal(r)
	if strings.Contains(strings.ToLower(string(b)), "kubeconfig") {
		t.Fatal("kubernetes credential leaked")
	}
}
func TestAgentResponse_NeverContainsOpenBaoToken(t *testing.T) {
	f := newFixture(t, agentv1.BudgetPolicy{})
	r := invokeOK(t, f, f.request(agentv1.ToolSetSecret, application.SetSecretArguments{ApplicationID: "app-1", EnvironmentID: "env-1", Name: "API_TOKEN", Value: "openbao-root-token"}, "k"))
	b, _ := json.Marshal(r)
	if strings.Contains(string(b), "openbao-root-token") {
		t.Fatal("token leaked")
	}
}
func TestAgentLogs_RedactRepositoryWriteCredential(t *testing.T) {
	f := newFixture(t, agentv1.BudgetPolicy{})
	r := invokeOK(t, f, f.request(agentv1.ToolGetLogs, application.GetLogsArguments{ApplicationID: "app-1", Limit: 100}, "k"))
	if strings.Contains(string(r.Result.Data), "topsecret") {
		t.Fatal("log secret leaked")
	}
}
func TestAgentWorkspaceCredentialExpiresAfterOperation(t *testing.T) {
	f := newFixture(t, agentv1.BudgetPolicy{})
	f.clock.Advance(2 * time.Hour)
	_, err := f.svc.Invoke(context.Background(), f.request(agentv1.ToolCreateProject, application.CreateProjectArguments{Name: "x"}, "k"))
	if errCode(err) != domain.CodePermissionDenied {
		t.Fatalf("err=%v", err)
	}
}

func TestAgent_CanSetSecretWithScope(t *testing.T) {
	f := newFixture(t, agentv1.BudgetPolicy{}, string(agentv1.ScopeForTool(agentv1.ToolSetSecret)))
	invokeOK(t, f, f.request(agentv1.ToolSetSecret, application.SetSecretArguments{ApplicationID: "app-1", EnvironmentID: "env-1", Name: "API_KEY", Value: "s3cr3t"}, "k"))
}
func TestAgent_CannotReadSecretValue(t *testing.T) {
	f := newFixture(t, agentv1.BudgetPolicy{})
	invokeOK(t, f, f.request(agentv1.ToolSetSecret, application.SetSecretArguments{ApplicationID: "app-1", EnvironmentID: "env-1", Name: "API_KEY", Value: "s3cr3t"}, "k1"))
	r := invokeOK(t, f, f.request(agentv1.ToolListSecretMetadata, application.ListSecretMetadataArguments{ApplicationID: "app-1", EnvironmentID: "env-1"}, "k2"))
	if strings.Contains(string(r.Result.Data), "s3cr3t") {
		t.Fatal("secret value returned")
	}
}
func TestAgent_ListSecretsReturnsMetadataOnly(t *testing.T) {
	f := newFixture(t, agentv1.BudgetPolicy{})
	invokeOK(t, f, f.request(agentv1.ToolSetSecret, application.SetSecretArguments{ApplicationID: "app-1", EnvironmentID: "env-1", Name: "API_KEY", Value: "s3cr3t"}, "k1"))
	r := invokeOK(t, f, f.request(agentv1.ToolListSecretMetadata, application.ListSecretMetadataArguments{ApplicationID: "app-1", EnvironmentID: "env-1"}, "k2"))
	if !strings.Contains(string(r.Result.Data), "API_KEY") || strings.Contains(string(r.Result.Data), "value") {
		t.Fatalf("data=%s", r.Result.Data)
	}
}
func TestAgent_SecretValueAbsentFromToolInvocationAudit(t *testing.T) {
	f := newFixture(t, agentv1.BudgetPolicy{})
	invokeOK(t, f, f.request(agentv1.ToolSetSecret, application.SetSecretArguments{ApplicationID: "app-1", EnvironmentID: "env-1", Name: "API_KEY", Value: "audit-secret"}, "k"))
	a, _ := f.svc.AuditTrail(context.Background(), f.tenant, f.task)
	b, _ := json.Marshal(a)
	if strings.Contains(string(b), "audit-secret") {
		t.Fatal("secret leaked to audit")
	}
}
func TestAgent_SecretValueAbsentFromErrorOnProviderFailure(t *testing.T) {
	f := newFixture(t, agentv1.BudgetPolicy{})
	f.attachments.Fail = fmt.Errorf("provider failed value=error-secret")
	r, err := f.svc.Invoke(context.Background(), f.request(agentv1.ToolSetSecret, application.SetSecretArguments{ApplicationID: "app-1", EnvironmentID: "env-1", Name: "API_KEY", Value: "error-secret"}, "k"))
	if err == nil {
		t.Fatal("expected error")
	}
	b, _ := json.Marshal(r)
	if strings.Contains(string(b), "error-secret") {
		t.Fatal("secret leaked")
	}
}

func TestApproval_RequestContainsActionResourceAndPayloadHash(t *testing.T) {
	f := newFixture(t, agentv1.BudgetPolicy{})
	a := deployArgs("production", 1)
	p, _ := json.Marshal(a)
	r := invokeOK(t, f, f.request(agentv1.ToolRequestApproval, application.RequestApprovalArguments{Action: agentv1.ApprovalDeployProduction, Resource: agentv1.ApprovalResource{Type: "environment", ID: a.EnvironmentID}, Payload: p, TTLSeconds: 60}, "k"))
	var v agentv1.ApprovalRequestView
	_ = json.Unmarshal(r.Result.Data, &v)
	if v.Action != agentv1.ApprovalDeployProduction || v.Resource.ID != a.EnvironmentID || !strings.HasPrefix(v.PayloadHash, "sha256:") {
		t.Fatalf("view=%+v", v)
	}
}
func TestApproval_GrantAllowsExactlyMatchingAction(t *testing.T) {
	f := newFixture(t, agentv1.BudgetPolicy{})
	seedBuild(f)
	a := deployArgs("production", 1)
	g := approvalFor(t, f, a)
	req := f.request(agentv1.ToolDeploy, a, "deploy")
	req.ApprovalGrantID = g
	invokeOK(t, f, req)
}
func TestApproval_GrantCannotBeReused(t *testing.T) {
	f := newFixture(t, agentv1.BudgetPolicy{})
	seedBuild(f)
	a := deployArgs("production", 1)
	g := approvalFor(t, f, a)
	r := f.request(agentv1.ToolDeploy, a, "d1")
	r.ApprovalGrantID = g
	invokeOK(t, f, r)
	r = f.request(agentv1.ToolDeploy, a, "d2")
	r.ApprovalGrantID = g
	if _, err := f.svc.Invoke(context.Background(), r); err == nil {
		t.Fatal("grant reused")
	}
}
func TestApproval_GrantExpires(t *testing.T) {
	f := newFixture(t, agentv1.BudgetPolicy{})
	seedBuild(f)
	a := deployArgs("production", 1)
	g := approvalFor(t, f, a)
	f.clock.Advance(20 * time.Minute)
	r := f.request(agentv1.ToolDeploy, a, "d")
	r.ApprovalGrantID = g
	if _, err := f.svc.Invoke(context.Background(), r); err == nil {
		t.Fatal("expired grant accepted")
	}
}
func TestApproval_GrantCannotBeUsedByDifferentAgent(t *testing.T) {
	f := newFixture(t, agentv1.BudgetPolicy{})
	seedBuild(f)
	a := deployArgs("production", 1)
	g := approvalFor(t, f, a)
	r := f.request(agentv1.ToolDeploy, a, "d")
	r.AgentID = "agent-2"
	r.ApprovalGrantID = g
	if _, err := f.svc.Invoke(context.Background(), r); err == nil {
		t.Fatal("cross agent grant accepted")
	}
}
func TestApproval_GrantCannotBeUsedForDifferentPayload(t *testing.T) {
	f := newFixture(t, agentv1.BudgetPolicy{})
	seedBuild(f)
	a := deployArgs("production", 1)
	g := approvalFor(t, f, a)
	a.Configuration.Unit = "u2"
	r := f.request(agentv1.ToolDeploy, a, "d")
	r.ApprovalGrantID = g
	if _, err := f.svc.Invoke(context.Background(), r); err == nil {
		t.Fatal("altered payload accepted")
	}
}
func TestApproval_DenialLeavesNoSideEffect(t *testing.T) {
	f := newFixture(t, agentv1.BudgetPolicy{})
	seedBuild(f)
	a := deployArgs("production", 1)
	p, _ := json.Marshal(a)
	r := invokeOK(t, f, f.request(agentv1.ToolRequestApproval, application.RequestApprovalArguments{Action: agentv1.ApprovalDeployProduction, Resource: agentv1.ApprovalResource{Type: "environment", ID: a.EnvironmentID}, Payload: p, TTLSeconds: 60}, "k"))
	var v agentv1.ApprovalRequestView
	_ = json.Unmarshal(r.Result.Data, &v)
	if err := f.svc.DenyApprovalForTenant(context.Background(), f.tenant, v.ApprovalRequestID, "approver-1", "user"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Invoke(context.Background(), f.request(agentv1.ToolDeploy, a, "d")); err == nil || f.runtime.DeployCalls != 0 {
		t.Fatalf("err=%v calls=%d", err, f.runtime.DeployCalls)
	}
}

func TestBudget_BuildCountLimitStopsAdditionalBuild(t *testing.T) {
	f := newFixture(t, agentv1.BudgetPolicy{MaxBuildCount: 1, MaxBuildMinutes: 100, MaxDeployCount: 10, RepairThreshold: 3})
	invokeOK(t, f, f.request(agentv1.ToolRequestBuild, buildArgs(1), "b1"))
	if _, err := f.svc.Invoke(context.Background(), f.request(agentv1.ToolRequestBuild, buildArgs(1), "b2")); errCode(err) != domain.CodeBudgetExceeded {
		t.Fatalf("err=%v", err)
	}
}
func TestBudget_BuildMinutesLimitStopsLongLoop(t *testing.T) {
	f := newFixture(t, agentv1.BudgetPolicy{MaxBuildCount: 10, MaxBuildMinutes: 2, MaxDeployCount: 10, RepairThreshold: 3})
	if _, err := f.svc.Invoke(context.Background(), f.request(agentv1.ToolRequestBuild, buildArgs(3), "b")); errCode(err) != domain.CodeBudgetExceeded {
		t.Fatalf("err=%v", err)
	}
}
func TestBudget_DeployCountLimitStopsRepeatedDeploy(t *testing.T) {
	f := newFixture(t, agentv1.BudgetPolicy{MaxBuildCount: 10, MaxBuildMinutes: 100, MaxDeployCount: 1, RepairThreshold: 3})
	seedBuild(f)
	invokeOK(t, f, f.request(agentv1.ToolDeploy, deployArgs("staging", 1), "d1"))
	if _, err := f.svc.Invoke(context.Background(), f.request(agentv1.ToolDeploy, deployArgs("staging", 2), "d2")); errCode(err) != domain.CodeBudgetExceeded {
		t.Fatalf("err=%v", err)
	}
}
func TestBudget_ResetRequiresNewTaskOrExplicitPolicy(t *testing.T) {
	f := newFixture(t, agentv1.BudgetPolicy{MaxBuildCount: 1, MaxBuildMinutes: 10, MaxDeployCount: 1, RepairThreshold: 2})
	invokeOK(t, f, f.request(agentv1.ToolRequestBuild, buildArgs(1), "b1"))
	if _, err := f.svc.Invoke(context.Background(), f.request(agentv1.ToolRequestBuild, buildArgs(1), "b2")); err == nil {
		t.Fatal("budget reset implicitly")
	}
}
func TestBudget_UsageIsAtomicUnderConcurrentToolCalls(t *testing.T) {
	f := newFixture(t, agentv1.BudgetPolicy{MaxBuildCount: 3, MaxBuildMinutes: 3, MaxDeployCount: 10, RepairThreshold: 3})
	var wg sync.WaitGroup
	success := int64(0)
	var mu sync.Mutex
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if _, err := f.svc.Invoke(context.Background(), f.request(agentv1.ToolRequestBuild, buildArgs(1), fmt.Sprintf("b-%d", i))); err == nil {
				mu.Lock()
				success++
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()
	if success != 3 {
		t.Fatalf("success=%d", success)
	}
}

func TestRepairLoop_SameFailureFingerprintIncrementsCounter(t *testing.T) {
	f := newFixture(t, agentv1.BudgetPolicy{})
	_ = f.svc.RecordFailure(context.Background(), f.tenant, f.task, "compile:x", false)
	_ = f.svc.RecordFailure(context.Background(), f.tenant, f.task, "compile:x", false)
	v, _ := f.svc.GetTask(context.Background(), f.tenant, f.task)
	if v.RepairCount != 2 {
		t.Fatalf("count=%d", v.RepairCount)
	}
}
func TestRepairLoop_DifferentFailureResetsOrBranchesCounterByPolicy(t *testing.T) {
	f := newFixture(t, agentv1.BudgetPolicy{})
	_ = f.svc.RecordFailure(context.Background(), f.tenant, f.task, "x", false)
	_ = f.svc.RecordFailure(context.Background(), f.tenant, f.task, "y", false)
	v, _ := f.svc.GetTask(context.Background(), f.tenant, f.task)
	if v.RepairCount != 1 || v.RepairFingerprint != "y" {
		t.Fatalf("view=%+v", v)
	}
}
func TestRepairLoop_ThresholdPausesAutonomy(t *testing.T) {
	f := newFixture(t, agentv1.BudgetPolicy{MaxBuildCount: 10, MaxBuildMinutes: 10, MaxDeployCount: 10, RepairThreshold: 2})
	_ = f.svc.RecordFailure(context.Background(), f.tenant, f.task, "x", false)
	_ = f.svc.RecordFailure(context.Background(), f.tenant, f.task, "x", false)
	v, _ := f.svc.GetTask(context.Background(), f.tenant, f.task)
	if v.State != "PAUSED" {
		t.Fatalf("state=%s", v.State)
	}
}
func TestRepairLoop_PauseRequiresUserDecision(t *testing.T) {
	f := newFixture(t, agentv1.BudgetPolicy{MaxBuildCount: 10, MaxBuildMinutes: 10, MaxDeployCount: 10, RepairThreshold: 1})
	_ = f.svc.RecordFailure(context.Background(), f.tenant, f.task, "x", false)
	if _, err := f.svc.Invoke(context.Background(), f.request(agentv1.ToolCreateProject, application.CreateProjectArguments{Name: "x"}, "k")); errCode(err) != domain.CodePaused {
		t.Fatalf("err=%v", err)
	}
	if err := f.svc.ResumeTask(context.Background(), f.tenant, f.task, "user-1"); err != nil {
		t.Fatal(err)
	}
	invokeOK(t, f, f.request(agentv1.ToolCreateProject, application.CreateProjectArguments{Name: "x"}, "k2"))
}
func TestRepairLoop_PlatformFailureDoesNotConsumeUserRepairBudget(t *testing.T) {
	f := newFixture(t, agentv1.BudgetPolicy{})
	_ = f.svc.RecordFailure(context.Background(), f.tenant, f.task, "platform", true)
	v, _ := f.svc.GetTask(context.Background(), f.tenant, f.task)
	if v.RepairCount != 0 {
		t.Fatal("platform failure consumed budget")
	}
}

func TestAgentCreateProject_ReplayReturnsSameProject(t *testing.T) {
	f := newFixture(t, agentv1.BudgetPolicy{})
	req := f.request(agentv1.ToolCreateProject, application.CreateProjectArguments{Name: "x"}, "same")
	a := invokeOK(t, f, req)
	b := invokeOK(t, f, req)
	if a.Result.ID != b.Result.ID || !b.Replayed || f.source.CreateCalls != 1 {
		t.Fatalf("a=%+v b=%+v calls=%d", a, b, f.source.CreateCalls)
	}
}
func TestAgentBuild_WaitsThroughOperationContractNotInternalPolling(t *testing.T) {
	f := newFixture(t, agentv1.BudgetPolicy{})
	f.builds.TimeoutOnce = true
	r := invokeOK(t, f, f.request(agentv1.ToolRequestBuild, buildArgs(1), "b"))
	if r.Result == nil || f.builds.ResumeCalls != 1 {
		t.Fatalf("r=%+v resumes=%d", r, f.builds.ResumeCalls)
	}
}
func TestAgentDeploy_UsesArtifactReturnedByBuild(t *testing.T) {
	f := newFixture(t, agentv1.BudgetPolicy{})
	seedBuild(f)
	invokeOK(t, f, f.request(agentv1.ToolDeploy, deployArgs("staging", 1), "d"))
	if f.runtime.LastDeployRequest.Artifact.Digest == "" || f.runtime.LastDeployRequest.Artifact.Digest != "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Fatalf("request=%+v", f.runtime.LastDeployRequest)
	}
}
func TestAgentProvisionAndBind_UsesPublishedAttachmentContracts(t *testing.T) {
	f := newFixture(t, agentv1.BudgetPolicy{})
	s := invokeOK(t, f, f.request(agentv1.ToolProvisionService, application.ProvisionServiceArguments{ServiceType: "postgres", Plan: "small", Name: "db"}, "p"))
	invokeOK(t, f, f.request(agentv1.ToolBindService, application.BindServiceArguments{ServiceInstanceID: s.Result.ID, ApplicationID: "app-1", EnvironmentID: "env-1"}, "b"))
}
func TestAgentCancellation_PropagatesToCancelableOperation(t *testing.T) {
	f := newFixture(t, agentv1.BudgetPolicy{})
	invokeOK(t, f, f.request(agentv1.ToolCancelOperation, application.CancelOperationArguments{OperationID: "op-1"}, "c"))
	if !f.ops.Canceled["op-1"] {
		t.Fatal("not canceled")
	}
}
func TestAgentConcurrentDeploys_UseExpectedEnvironmentRevision(t *testing.T) {
	f := newFixture(t, agentv1.BudgetPolicy{})
	seedBuild(f)
	invokeOK(t, f, f.request(agentv1.ToolDeploy, deployArgs("staging", 7), "d"))
	if f.runtime.LastExpectedRevision != 7 {
		t.Fatalf("revision=%d", f.runtime.LastExpectedRevision)
	}
}

func TestAgentAction_EntitlementCheckedBeforeSideEffect(t *testing.T) {
	f := newFixture(t, agentv1.BudgetPolicy{})
	f.commerce.Allowed = false
	if _, err := f.svc.Invoke(context.Background(), f.request(agentv1.ToolCreateProject, application.CreateProjectArguments{Name: "x"}, "k")); errCode(err) != domain.CodeEntitlementDenied || f.source.CreateCalls != 0 {
		t.Fatalf("err=%v calls=%d", err, f.source.CreateCalls)
	}
}
func TestAgentAction_QuotaRejectionIsExplainedWithoutInternalData(t *testing.T) {
	f := newFixture(t, agentv1.BudgetPolicy{})
	f.commerce.Allowed = false
	f.commerce.Reason = "sql row plan-v1 secret"
	r, err := f.svc.Invoke(context.Background(), f.request(agentv1.ToolCreateProject, application.CreateProjectArguments{Name: "x"}, "k"))
	if err == nil || strings.Contains(string(mustJSON(r)), "sql row") {
		t.Fatalf("r=%+v err=%v", r, err)
	}
}
func TestAgentAction_CannotApproveOwnPaidUpgrade(t *testing.T) {
	f := newFixture(t, agentv1.BudgetPolicy{})
	payload := json.RawMessage(`{"plan":"business"}`)
	r := invokeOK(t, f, f.request(agentv1.ToolRequestApproval, application.RequestApprovalArguments{Action: agentv1.ApprovalIncreasePaidPlan, Resource: agentv1.ApprovalResource{Type: "subscription", ID: "sub-1"}, Payload: payload, TTLSeconds: 60}, "a"))
	var v agentv1.ApprovalRequestView
	_ = json.Unmarshal(r.Result.Data, &v)
	if _, err := f.svc.GrantApprovalForTenant(context.Background(), f.tenant, v.ApprovalRequestID, f.agent, "agent"); err == nil {
		t.Fatal("agent approved itself")
	}
}
func TestAgentAction_SuspendedTenantCanReadStatusButCannotMutate(t *testing.T) {
	f := newFixture(t, agentv1.BudgetPolicy{})
	f.commerce.Allowed = false
	f.source.Projects["project-1"] = application.ProjectRef{ProjectID: "project-1", State: "READY"}
	invokeOK(t, f, f.request(agentv1.ToolGetProject, application.GetProjectArguments{ProjectID: "project-1"}, "r"))
	if _, err := f.svc.Invoke(context.Background(), f.request(agentv1.ToolCreateProject, application.CreateProjectArguments{Name: "x"}, "w")); errCode(err) != domain.CodeEntitlementDenied {
		t.Fatalf("err=%v", err)
	}
}

func TestAgentAudit_ContainsTaskToolActorUserAndCorrelation(t *testing.T) {
	f := newFixture(t, agentv1.BudgetPolicy{})
	invokeOK(t, f, f.request(agentv1.ToolCreateProject, application.CreateProjectArguments{Name: "x"}, "k"))
	a, _ := f.svc.AuditTrail(context.Background(), f.tenant, f.task)
	if len(a) == 0 || a[len(a)-1].AgentID != f.agent || a[len(a)-1].OnBehalfOfUserID != f.user || a[len(a)-1].CorrelationID != "corr-1" {
		t.Fatalf("audit=%+v", a)
	}
}
func TestAgentAudit_ConnectsCommitBuildReleaseDeployment(t *testing.T) {
	f := newFixture(t, agentv1.BudgetPolicy{})
	f.source.Projects["project-1"] = application.ProjectRef{ProjectID: "project-1", RepositoryID: "repo-project-1", State: "READY"}
	invokeOK(t, f, f.request(agentv1.ToolApplyRepositoryPatch, application.ApplyPatchArguments{ProjectID: "project-1", BaseCommitSHA: "abcdef0", Branch: "main", Message: "change", Files: []application.PatchFile{{Path: "main.go", Content: "package main"}}}, "p"))
	b := invokeOK(t, f, f.request(agentv1.ToolRequestBuild, buildArgs(1), "b"))
	var bv buildv1.BuildView
	_ = json.Unmarshal(b.Result.Data, &bv)
	a := deployArgs("staging", 1)
	a.BuildID = bv.BuildID
	invokeOK(t, f, f.request(agentv1.ToolDeploy, a, "d"))
	audit, _ := f.svc.AuditTrail(context.Background(), f.tenant, f.task)
	seen := map[agentv1.Tool]bool{}
	for _, x := range audit {
		seen[x.Tool] = true
	}
	for _, tool := range []agentv1.Tool{agentv1.ToolApplyRepositoryPatch, agentv1.ToolRequestBuild, agentv1.ToolDeploy} {
		if !seen[tool] {
			t.Fatalf("missing %s", tool)
		}
	}
}
func TestAgentAudit_RedactsSecretsAndTokens(t *testing.T) {
	f := newFixture(t, agentv1.BudgetPolicy{})
	invokeOK(t, f, f.request(agentv1.ToolSetSecret, application.SetSecretArguments{ApplicationID: "app-1", EnvironmentID: "env-1", Name: "TOKEN", Value: "audit-supersecret"}, "k"))
	a, _ := f.svc.AuditTrail(context.Background(), f.tenant, f.task)
	if strings.Contains(string(mustJSON(a)), "audit-supersecret") {
		t.Fatal("leak")
	}
}
func TestAgentAudit_FailedAuthorizationRecorded(t *testing.T) {
	f := newFixture(t, agentv1.BudgetPolicy{}, string(agentv1.ScopeForTool(agentv1.ToolGetProject)))
	_, _ = f.svc.Invoke(context.Background(), f.request(agentv1.ToolCreateProject, application.CreateProjectArguments{Name: "x"}, "k"))
	a, _ := f.svc.AuditTrail(context.Background(), f.tenant, f.task)
	if len(a) == 0 || a[len(a)-1].Outcome != "DENIED" {
		t.Fatalf("audit=%+v", a)
	}
}
func TestAgentAudit_OperationReplayReferencesOriginalInvocation(t *testing.T) {
	f := newFixture(t, agentv1.BudgetPolicy{})
	f.ops.ByID["op-1"] = kernelv1.OperationSnapshot{OperationRef: kernelv1.OperationRef{OperationID: "op-1", State: kernelv1.OperationRunning}}
	req := f.request(agentv1.ToolGetOperation, application.GetOperationArguments{OperationID: "op-1"}, "same")
	a := invokeOK(t, f, req)
	b := invokeOK(t, f, req)
	if a.InvocationID != b.InvocationID || !b.Replayed {
		t.Fatalf("a=%+v b=%+v", a, b)
	}
}
func mustJSON(v any) []byte { b, _ := json.Marshal(v); return b }

func TestAgentFlow_LostGitLabWebhookRecoveredBySourceReconciler(t *testing.T) {
	f := newFixture(t, agentv1.BudgetPolicy{})
	f.source.LostWebhook = true
	invokeOK(t, f, f.request(agentv1.ToolApplyRepositoryPatch, application.ApplyPatchArguments{ProjectID: "project-1", BaseCommitSHA: "abcdef0", Branch: "main", Message: "x", Files: []application.PatchFile{{Path: "main.go", Content: "x"}}}, "p"))
	if f.source.ReconcileCalls != 1 || f.source.LostWebhook {
		t.Fatal("not reconciled")
	}
}
func TestAgentFlow_BuildProviderTimeoutResumesOperation(t *testing.T) {
	f := newFixture(t, agentv1.BudgetPolicy{})
	f.builds.TimeoutOnce = true
	invokeOK(t, f, f.request(agentv1.ToolRequestBuild, buildArgs(1), "b"))
	if f.builds.RequestCalls != 1 || f.builds.ResumeCalls != 1 {
		t.Fatalf("request=%d resume=%d", f.builds.RequestCalls, f.builds.ResumeCalls)
	}
}
func TestAgentFlow_GitOpsCommitResponseLostIsReconciled(t *testing.T) {
	f := newFixture(t, agentv1.BudgetPolicy{})
	seedBuild(f)
	f.runtime.TimeoutOnce = true
	invokeOK(t, f, f.request(agentv1.ToolDeploy, deployArgs("staging", 1), "d"))
	if f.runtime.ReconcileCalls != 1 || f.runtime.DeployCalls != 2 {
		t.Fatalf("reconcile=%d deploy=%d", f.runtime.ReconcileCalls, f.runtime.DeployCalls)
	}
}
func TestAgentFlow_ArgoUnavailableLeavesDeploymentPendingNotDuplicated(t *testing.T) {
	f := newFixture(t, agentv1.BudgetPolicy{})
	seedBuild(f)
	f.runtime.TimeoutOnce = true
	req := f.request(agentv1.ToolDeploy, deployArgs("staging", 1), "d")
	a := invokeOK(t, f, req)
	b := invokeOK(t, f, req)
	if a.Result.ID != b.Result.ID || len(f.runtime.ByID) != 1 {
		t.Fatalf("a=%+v b=%+v deployments=%d", a, b, len(f.runtime.ByID))
	}
}
func TestAgentFlow_StatusEventLostRecoveredByRuntimeReconciler(t *testing.T) {
	f := newFixture(t, agentv1.BudgetPolicy{})
	seedBuild(f)
	d := invokeOK(t, f, f.request(agentv1.ToolDeploy, deployArgs("staging", 1), "d"))
	f.runtime.StatusLost = true
	invokeOK(t, f, f.request(agentv1.ToolGetDeployment, application.GetDeploymentArguments{DeploymentID: d.Result.ID}, "s"))
	if f.runtime.ReconcileCalls != 1 {
		t.Fatalf("reconcile=%d", f.runtime.ReconcileCalls)
	}
}
