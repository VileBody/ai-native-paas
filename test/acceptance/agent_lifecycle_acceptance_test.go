package acceptance_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/keir-research/ai-native-paas/internal/agent/application"
	"github.com/keir-research/ai-native-paas/internal/agent/memory"
	"github.com/keir-research/ai-native-paas/internal/agent/testkit"
	agentv1 "github.com/keir-research/ai-native-paas/pkg/contracts/agent/v1"
	buildv1 "github.com/keir-research/ai-native-paas/pkg/contracts/build/v1"
	runtimev1 "github.com/keir-research/ai-native-paas/pkg/contracts/runtime/v1"
)

type lifecycleFixture struct {
	svc         *application.Service
	attachments *testkit.Attachments
	source      *testkit.Source
	runtime     *testkit.Runtime
}

func newLifecycleFixture(t *testing.T) *lifecycleFixture {
	t.Helper()
	clock := &testkit.Clock{T: time.Date(2026, 7, 13, 9, 0, 0, 0, time.UTC)}
	ids := &testkit.IDs{}
	source := testkit.NewSource()
	builds := testkit.NewBuilds()
	runtime := testkit.NewRuntime()
	attachments := testkit.NewAttachments()
	svc := &application.Service{Store: memory.New(), Clock: clock, IDs: ids, Source: source, Builds: builds, Runtime: runtime, Attachments: attachments, Commerce: &testkit.Commerce{Allowed: true}, Operations: testkit.NewOperations(), Logs: &testkit.Logs{Lines: []string{"ready", "password=must-redact"}}, Usage: &testkit.Usage{}}
	scopes := []string{}
	for _, tool := range agentv1.ToolCatalog() {
		scopes = append(scopes, string(agentv1.ScopeForTool(tool)))
	}
	if _, err := svc.RegisterPrincipal(context.Background(), application.RegisterPrincipalCommand{ID: "agent-booking", TenantID: "tenant-acme", OnBehalfOfUserID: "owner-acme", Scopes: scopes, CredentialExpiresAt: clock.T.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.StartTask(context.Background(), application.StartTaskCommand{ID: "task-booking", TenantID: "tenant-acme", AgentID: "agent-booking", OnBehalfOfUserID: "owner-acme", CorrelationID: "corr-booking", BudgetPolicy: agentv1.BudgetPolicy{MaxBuildCount: 5, MaxBuildMinutes: 30, MaxDeployCount: 3, RepairThreshold: 3}}); err != nil {
		t.Fatal(err)
	}
	return &lifecycleFixture{svc: svc, attachments: attachments, source: source, runtime: runtime}
}

func (f *lifecycleFixture) invoke(t *testing.T, tool agentv1.Tool, args any, key, grant string) agentv1.InvocationResponse {
	t.Helper()
	raw, _ := json.Marshal(args)
	response, err := f.svc.Invoke(context.Background(), agentv1.InvocationRequest{APIVersion: agentv1.APIVersion, SemanticsVersion: agentv1.SemanticsVersion, TenantID: "tenant-acme", AgentID: "agent-booking", TaskID: "task-booking", Tool: tool, Arguments: raw, IdempotencyKey: key, CorrelationID: "corr-booking", ApprovalGrantID: grant})
	if err != nil {
		t.Fatalf("%s: %v response=%+v", tool, err, response)
	}
	return response
}

func TestAcceptance_AgentCreatesBuildsAndDeploysProductionApplicationSafely(t *testing.T) {
	f := newLifecycleFixture(t)
	project := f.invoke(t, agentv1.ToolCreateProject, application.CreateProjectArguments{Name: "booking"}, "create-project", "")
	if project.Result == nil || project.Result.Type != "project" {
		t.Fatalf("project=%+v", project)
	}
	projectID := project.Result.ID

	commit := f.invoke(t, agentv1.ToolApplyRepositoryPatch, application.ApplyPatchArguments{ProjectID: projectID, BaseCommitSHA: "abcdef0123456789", Branch: "main", Message: "initial service", Files: []application.PatchFile{{Path: "go.mod", Content: "module booking"}, {Path: "main.go", Content: "package main"}}}, "commit-service", "")
	var commitRef application.CommitRef
	if err := json.Unmarshal(commit.Result.Data, &commitRef); err != nil {
		t.Fatal(err)
	}
	if commitRef.CommitSHA == "" || f.source.ReconcileCalls != 1 {
		t.Fatalf("commit=%+v reconciles=%d", commitRef, f.source.ReconcileCalls)
	}

	var buildArgs application.RequestBuildArguments
	buildArgs.Revision.ProjectID = projectID
	buildArgs.Revision.RepositoryID = commitRef.RepositoryID
	buildArgs.Revision.Branch = commitRef.Branch
	buildArgs.Revision.CommitSHA = commitRef.CommitSHA
	buildArgs.EstimatedMinutes = 5
	build := f.invoke(t, agentv1.ToolRequestBuild, buildArgs, "build-service", "")
	var buildView buildv1.BuildView
	if err := json.Unmarshal(build.Result.Data, &buildView); err != nil {
		t.Fatal(err)
	}
	if buildView.Artifact == nil || buildView.Artifact.Validate() != nil {
		t.Fatalf("build=%+v", buildView)
	}

	service := f.invoke(t, agentv1.ToolProvisionService, application.ProvisionServiceArguments{ServiceType: "postgres", Plan: "small", Name: "primary"}, "provision-db", "")
	binding := f.invoke(t, agentv1.ToolBindService, application.BindServiceArguments{ServiceInstanceID: service.Result.ID, ApplicationID: "booking-api", EnvironmentID: "env-production"}, "bind-db", "")
	if binding.Result == nil || binding.Result.Type != "service_binding" {
		t.Fatalf("binding=%+v", binding)
	}
	secretValue := "postgres://booking:secret@db/booking"
	secret := f.invoke(t, agentv1.ToolSetSecret, application.SetSecretArguments{ApplicationID: "booking-api", EnvironmentID: "env-production", Name: "DATABASE_URL", Value: secretValue}, "set-database-url", "")
	if strings.Contains(string(secret.Result.Data), secretValue) {
		t.Fatal("write-only secret leaked")
	}

	deployArgs := application.DeployArguments{BuildID: buildView.BuildID, ApplicationID: "booking-api", EnvironmentID: "env-production", EnvironmentName: "production", ExpectedEnvironmentRevision: 1, Configuration: runtimev1.ReleaseConfig{Region: "eu1", Isolation: runtimev1.IsolationSandboxed, Unit: "u1", Processes: map[string]runtimev1.ProcessSpec{"web": {Port: 8080, MinReplicas: 1, MaxReplicas: 2, HealthPath: "/health"}}, GeneratedHostname: "booking.apps.example.test", AttachmentSnapshotRef: "snapshot-1", RolloutTimeoutSeconds: 300, EgressProfile: "public-default"}}
	deployPayload, _ := json.Marshal(deployArgs)
	approval := f.invoke(t, agentv1.ToolRequestApproval, application.RequestApprovalArguments{Action: agentv1.ApprovalDeployProduction, Resource: agentv1.ApprovalResource{Type: "environment", ID: "env-production"}, Payload: deployPayload, TTLSeconds: 600}, "request-production-approval", "")
	var requestView agentv1.ApprovalRequestView
	if err := json.Unmarshal(approval.Result.Data, &requestView); err != nil {
		t.Fatal(err)
	}
	grant, err := f.svc.GrantApprovalForTenant(context.Background(), "tenant-acme", requestView.ApprovalRequestID, "owner-acme", "user")
	if err != nil {
		t.Fatal(err)
	}
	deployment := f.invoke(t, agentv1.ToolDeploy, deployArgs, "deploy-production", grant.ID)
	if deployment.Result == nil || deployment.Result.Type != "deployment" {
		t.Fatalf("deployment=%+v", deployment)
	}
	status := f.invoke(t, agentv1.ToolGetDeployment, application.GetDeploymentArguments{DeploymentID: deployment.Result.ID}, "deployment-status", "")
	if status.Result.URL == "" || status.Result.State != string(runtimev1.DeploymentReady) {
		t.Fatalf("status=%+v", status)
	}
	logs := f.invoke(t, agentv1.ToolGetLogs, application.GetLogsArguments{ApplicationID: "booking-api", Limit: 100}, "read-logs", "")
	if strings.Contains(string(logs.Result.Data), "must-redact") {
		t.Fatal("runtime log secret leaked")
	}

	audit, err := f.svc.AuditTrail(context.Background(), "tenant-acme", "task-booking")
	if err != nil {
		t.Fatal(err)
	}
	seen := map[agentv1.Tool]bool{}
	rawAudit, _ := json.Marshal(audit)
	if strings.Contains(string(rawAudit), secretValue) {
		t.Fatal("secret leaked to audit")
	}
	for _, event := range audit {
		seen[event.Tool] = true
		if event.AgentID != "agent-booking" || event.OnBehalfOfUserID != "owner-acme" || event.CorrelationID != "corr-booking" {
			t.Fatalf("audit identity=%+v", event)
		}
	}
	for _, tool := range []agentv1.Tool{agentv1.ToolCreateProject, agentv1.ToolApplyRepositoryPatch, agentv1.ToolRequestBuild, agentv1.ToolProvisionService, agentv1.ToolBindService, agentv1.ToolSetSecret, agentv1.ToolRequestApproval, agentv1.ToolDeploy, agentv1.ToolGetDeployment} {
		if !seen[tool] {
			t.Fatalf("audit missing %s", tool)
		}
	}
}

func TestAcceptance_AgentCannotDeployProductionWithoutApproval(t *testing.T) {
	f := newLifecycleFixture(t)
	artifact := buildv1.ArtifactRef{ArtifactID: "artifact-1", Repository: "registry.invalid/app", Digest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", MediaType: "application/vnd.oci.image.manifest.v1+json"}
	f.svc.Builds.(*testkit.Builds).ByID["build-1"] = buildv1.BuildView{BuildID: "build-1", TenantID: "tenant-acme", State: buildv1.BuildSucceeded, Artifact: &artifact}
	args := application.DeployArguments{BuildID: "build-1", ApplicationID: "app-1", EnvironmentID: "env-production", EnvironmentName: "production", ExpectedEnvironmentRevision: 1, Configuration: runtimev1.ReleaseConfig{Region: "eu1", Isolation: runtimev1.IsolationSandboxed, Unit: "u1", Processes: map[string]runtimev1.ProcessSpec{"web": {Port: 8080, MinReplicas: 1, MaxReplicas: 1}}}}
	raw, _ := json.Marshal(args)
	_, err := f.svc.Invoke(context.Background(), agentv1.InvocationRequest{APIVersion: agentv1.APIVersion, SemanticsVersion: agentv1.SemanticsVersion, TenantID: "tenant-acme", AgentID: "agent-booking", TaskID: "task-booking", Tool: agentv1.ToolDeploy, Arguments: raw, IdempotencyKey: "no-approval", CorrelationID: "corr-booking"})
	if err == nil || f.runtime.DeployCalls != 0 {
		t.Fatalf("err=%v deploy calls=%d", err, f.runtime.DeployCalls)
	}
}
