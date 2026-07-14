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
	agentv2 "github.com/keir-research/ai-native-paas/pkg/contracts/agent/v2"
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
	if _, err := svc.StartTask(context.Background(), application.StartTaskCommand{ID: "task-booking", TenantID: "tenant-acme", ProjectID: "project-1", AgentID: "agent-booking", OnBehalfOfUserID: "owner-acme", CorrelationID: "corr-booking", IntentID: "intent-booking", BudgetPolicy: agentv1.BudgetPolicy{MaxBuildCount: 5, MaxBuildMinutes: 30, MaxDeployCount: 3, RepairThreshold: 3}}); err != nil {
		t.Fatal(err)
	}
	return &lifecycleFixture{svc: svc, attachments: attachments, source: source, runtime: runtime}
}

func TestAgent_AuditConnectsIntentTaskCommandsCommitsPlansApprovalsAndRuntime(t *testing.T) {
	const secretSentinel = "G28_SECRET_SENTINEL_must_never_reach_audit"
	const planHash = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	f := newLifecycleFixture(t)
	record := func(tool agentv2.Tool, key string, evidence agentv1.AuditEvidence) {
		t.Helper()
		command := application.RecordTaskEvidenceCommand{
			TenantID: "tenant-acme", ProjectID: "project-1", AgentID: "agent-booking",
			TaskID: "task-booking", CorrelationID: "corr-booking", Tool: tool,
			IdempotencyKey: key, Evidence: evidence,
		}
		if err := f.svc.RecordTaskEvidence(context.Background(), command); err != nil {
			t.Fatalf("record %s: %v", tool, err)
		}
		// Producer retries must not duplicate evidence in the append-only trail.
		if err := f.svc.RecordTaskEvidence(context.Background(), command); err != nil {
			t.Fatalf("replay %s: %v", tool, err)
		}
	}

	record(agentv2.ToolWorkspaceCreate, "workspace-create", agentv1.AuditEvidence{WorkspaceID: "workspace-1"})
	record(agentv2.ToolWorkspaceExec, "workspace-checkout", agentv1.AuditEvidence{WorkspaceID: "workspace-1", WorkspaceCommandID: "command-checkout"})
	if err := f.svc.RecordTaskEvidence(context.Background(), application.RecordTaskEvidenceCommand{
		TenantID: "tenant-acme", ProjectID: "project-other", AgentID: "agent-booking", TaskID: "task-booking",
		CorrelationID: "corr-booking", Tool: agentv2.ToolWorkspaceGet, IdempotencyKey: "cross-project-evidence",
		Evidence: agentv1.AuditEvidence{WorkspaceID: "workspace-other"},
	}); err == nil {
		t.Fatal("cross-project evidence was accepted")
	}

	project := f.invoke(t, agentv1.ToolCreateProject, application.CreateProjectArguments{Name: "booking"}, "g28-project", "")
	commitResponse := f.invoke(t, agentv1.ToolApplyRepositoryPatch, application.ApplyPatchArguments{
		ProjectID: project.Result.ID, BaseCommitSHA: "abcdef0123456789", Branch: "main", Message: "implement booking API",
		Files: []application.PatchFile{{Path: "main.go", Content: "package main\n// " + secretSentinel}},
	}, "g28-commit", "")
	var commit application.CommitRef
	if err := json.Unmarshal(commitResponse.Result.Data, &commit); err != nil {
		t.Fatal(err)
	}

	var buildArguments application.RequestBuildArguments
	buildArguments.Revision.ProjectID = commit.ProjectID
	buildArguments.Revision.RepositoryID = commit.RepositoryID
	buildArguments.Revision.Branch = commit.Branch
	buildArguments.Revision.CommitSHA = commit.CommitSHA
	buildArguments.EstimatedMinutes = 2
	buildResponse := f.invoke(t, agentv1.ToolRequestBuild, buildArguments, "g28-build", "")
	var build buildv1.BuildView
	if err := json.Unmarshal(buildResponse.Result.Data, &build); err != nil || build.Artifact == nil {
		t.Fatalf("build=%+v err=%v", build, err)
	}

	record(agentv2.ToolInfraPlan, "infra-plan", agentv1.AuditEvidence{
		WorkspaceID: "workspace-1", WorkspaceCommandID: "command-plan", PlanID: "plan-1", PlanHash: planHash,
	})
	deployArguments := application.DeployArguments{
		BuildID: build.BuildID, ApplicationID: "booking-api", EnvironmentID: "env-production", EnvironmentName: "production",
		ExpectedEnvironmentRevision: 1,
		Configuration: runtimev1.ReleaseConfig{
			Region: "eu1", Isolation: runtimev1.IsolationSandboxed, Unit: "u1",
			Processes:         map[string]runtimev1.ProcessSpec{"web": {Port: 8080, MinReplicas: 1, MaxReplicas: 2, HealthPath: "/health"}},
			GeneratedHostname: "booking.apps.example.test", RolloutTimeoutSeconds: 300, EgressProfile: "public-default",
		},
	}
	approvalPayload, _ := json.Marshal(deployArguments)
	approvalResponse := f.invoke(t, agentv1.ToolRequestApproval, application.RequestApprovalArguments{
		Action: agentv1.ApprovalDeployProduction, Resource: agentv1.ApprovalResource{Type: "environment", ID: "env-production"},
		Payload: approvalPayload, TTLSeconds: 600,
	}, "g28-approval", "")
	var approval agentv1.ApprovalRequestView
	if err := json.Unmarshal(approvalResponse.Result.Data, &approval); err != nil {
		t.Fatal(err)
	}
	grant, err := f.svc.GrantApprovalForTenant(context.Background(), "tenant-acme", approval.ApprovalRequestID, "owner-acme", "user")
	if err != nil {
		t.Fatal(err)
	}
	record(agentv2.ToolApprovalGet, "plan-approval", agentv1.AuditEvidence{
		PlanID: "plan-1", PlanHash: planHash, ApprovalRequestID: approval.ApprovalRequestID, ApprovalGrantID: grant.ID,
	})
	record(agentv2.ToolInfraApply, "infra-apply", agentv1.AuditEvidence{
		WorkspaceID: "workspace-1", WorkspaceCommandID: "command-apply", PlanID: "plan-1", PlanHash: planHash,
		ApprovalGrantID: grant.ID, ApplyOperationID: "operation-apply-1",
	})

	deploymentResponse := f.invoke(t, agentv1.ToolDeploy, deployArguments, "g28-deploy", grant.ID)
	statusResponse := f.invoke(t, agentv1.ToolGetDeployment, application.GetDeploymentArguments{DeploymentID: deploymentResponse.Result.ID}, "g28-status", "")
	var status runtimev1.RuntimeStatus
	if err := json.Unmarshal(statusResponse.Result.Data, &status); err != nil || status.Phase != runtimev1.DeploymentReady {
		t.Fatalf("status=%+v err=%v", status, err)
	}

	audit, err := f.svc.AuditTrail(context.Background(), "tenant-acme", "task-booking")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(audit)
	if strings.Contains(string(raw), secretSentinel) {
		t.Fatal("raw workspace/source arguments leaked into audit")
	}
	for _, event := range audit {
		if event.TenantID != "tenant-acme" || event.TaskID != "task-booking" || event.AgentID != "agent-booking" || event.OnBehalfOfUserID != "owner-acme" || event.CorrelationID != "corr-booking" {
			t.Fatalf("broken audit scope: %+v", event)
		}
	}

	find := func(action string) agentv1.AuditEvidence {
		t.Helper()
		var found []agentv1.AuditEvidence
		for _, event := range audit {
			if event.Action == action {
				found = append(found, event.Evidence)
			}
		}
		if len(found) != 1 {
			t.Fatalf("action %s count=%d audit=%+v", action, len(found), audit)
		}
		return found[0]
	}
	if value := find("task_start"); value.IntentID != "intent-booking" {
		t.Fatalf("intent evidence=%+v", value)
	}
	if value := find(string(agentv2.ToolWorkspaceCreate)); value.WorkspaceID != "workspace-1" || value.ProjectID != "project-1" {
		t.Fatalf("workspace evidence=%+v", value)
	}
	if value := find(string(agentv2.ToolWorkspaceExec)); value.WorkspaceCommandID != "command-checkout" {
		t.Fatalf("workspace command evidence=%+v", value)
	}
	if value := find(string(agentv1.ToolApplyRepositoryPatch)); value.RepositoryID != commit.RepositoryID || value.CommitSHA != commit.CommitSHA {
		t.Fatalf("commit evidence=%+v commit=%+v", value, commit)
	}
	if value := find(string(agentv1.ToolRequestBuild)); value.CommitSHA != commit.CommitSHA || value.BuildID != build.BuildID || value.ArtifactDigest != build.Artifact.Digest {
		t.Fatalf("build evidence=%+v build=%+v", value, build)
	}
	if value := find(string(agentv2.ToolInfraPlan)); value.PlanID != "plan-1" || value.PlanHash != planHash || value.WorkspaceCommandID != "command-plan" {
		t.Fatalf("plan evidence=%+v", value)
	}
	if value := find("approval_grant"); value.ApprovalRequestID != approval.ApprovalRequestID || value.ApprovalGrantID != grant.ID {
		t.Fatalf("approval evidence=%+v", value)
	}
	if value := find(string(agentv2.ToolInfraApply)); value.PlanHash != planHash || value.ApprovalGrantID != grant.ID || value.WorkspaceCommandID != "command-apply" || value.ApplyOperationID != "operation-apply-1" {
		t.Fatalf("apply evidence=%+v", value)
	}
	if value := find(string(agentv1.ToolDeploy)); value.BuildID != build.BuildID || value.DeploymentID != status.DeploymentID || value.ReleaseID != status.ActiveRelease || value.GitOpsRevision != status.GitOpsRevision {
		t.Fatalf("deploy evidence=%+v status=%+v", value, status)
	}
	if value := find(string(agentv1.ToolGetDeployment)); value.DeploymentID != status.DeploymentID || value.GitOpsRevision != status.GitOpsRevision || value.ReadyEndpoint != status.URL {
		t.Fatalf("runtime evidence=%+v status=%+v", value, status)
	}
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

func TestAgent_HealthFailureCreatesNewPatchCommitNotDirectClusterFix(t *testing.T) {
	f := newLifecycleFixture(t)
	project := f.invoke(t, agentv1.ToolCreateProject, application.CreateProjectArguments{Name: "repairable-api"}, "repair-create-project", "")
	projectID := project.Result.ID

	patch := func(key, base, message, content string) application.CommitRef {
		response := f.invoke(t, agentv1.ToolApplyRepositoryPatch, application.ApplyPatchArguments{
			ProjectID: projectID, BaseCommitSHA: base, Branch: "main", Message: message,
			Files: []application.PatchFile{{Path: "main.go", Content: content}},
		}, key, "")
		var commit application.CommitRef
		if err := json.Unmarshal(response.Result.Data, &commit); err != nil {
			t.Fatal(err)
		}
		return commit
	}
	build := func(key string, commit application.CommitRef) buildv1.BuildView {
		var arguments application.RequestBuildArguments
		arguments.Revision.ProjectID = commit.ProjectID
		arguments.Revision.RepositoryID = commit.RepositoryID
		arguments.Revision.Branch = commit.Branch
		arguments.Revision.CommitSHA = commit.CommitSHA
		arguments.EstimatedMinutes = 2
		response := f.invoke(t, agentv1.ToolRequestBuild, arguments, key, "")
		var view buildv1.BuildView
		if err := json.Unmarshal(response.Result.Data, &view); err != nil {
			t.Fatal(err)
		}
		return view
	}
	configuration := runtimev1.ReleaseConfig{
		Region: "eu1", Isolation: runtimev1.IsolationSandboxed, Unit: "u1",
		Processes:         map[string]runtimev1.ProcessSpec{"web": {Port: 8080, MinReplicas: 1, MaxReplicas: 2, HealthPath: "/health"}},
		GeneratedHostname: "repairable.apps.example.test", RolloutTimeoutSeconds: 300, EgressProfile: "public-default",
	}
	deploy := func(key string, revision int64, buildID string) agentv1.InvocationResponse {
		return f.invoke(t, agentv1.ToolDeploy, application.DeployArguments{
			BuildID: buildID, ApplicationID: "repairable-api", EnvironmentID: "env-staging", EnvironmentName: "staging",
			ExpectedEnvironmentRevision: revision, Configuration: configuration,
		}, key, "")
	}

	initialCommit := patch("repair-initial-patch", "abcdef0", "initial implementation", "package main\nfunc healthy() bool { return false }\n")
	initialBuild := build("repair-initial-build", initialCommit)
	initialDeployment := deploy("repair-initial-deploy", 1, initialBuild.BuildID)
	if initialBuild.Identity != initialCommit.CommitSHA || initialBuild.Artifact == nil || initialDeployment.Result == nil {
		t.Fatalf("initial commit=%+v build=%+v deployment=%+v", initialCommit, initialBuild, initialDeployment)
	}
	if !f.runtime.SetStatus(initialDeployment.Result.ID, runtimev1.DeploymentFailed, 0) {
		t.Fatal("initial deployment status was not found")
	}
	failed := f.invoke(t, agentv1.ToolGetDeployment, application.GetDeploymentArguments{DeploymentID: initialDeployment.Result.ID}, "repair-observe-health-failure", "")
	if failed.Result.State != string(runtimev1.DeploymentFailed) || f.runtime.ReconcileCalls != 0 {
		t.Fatalf("failed status=%+v runtime_reconciles=%d", failed, f.runtime.ReconcileCalls)
	}

	repairCommit := patch("repair-followup-patch", initialCommit.CommitSHA, "fix health endpoint", "package main\nfunc healthy() bool { return true }\n")
	if repairCommit.CommitSHA == initialCommit.CommitSHA || repairCommit.CommitSHA == "" || len(f.source.PatchCommits) != 2 {
		t.Fatalf("initial=%+v repair=%+v commits=%+v", initialCommit, repairCommit, f.source.PatchCommits)
	}
	repairBuild := build("repair-followup-build", repairCommit)
	if repairBuild.Identity != repairCommit.CommitSHA || repairBuild.Artifact == nil || repairBuild.Artifact.Digest == initialBuild.Artifact.Digest {
		t.Fatalf("initial build=%+v repair build=%+v", initialBuild, repairBuild)
	}
	repairedDeployment := deploy("repair-followup-deploy", 2, repairBuild.BuildID)
	ready := f.invoke(t, agentv1.ToolGetDeployment, application.GetDeploymentArguments{DeploymentID: repairedDeployment.Result.ID}, "repair-observe-ready", "")
	if repairedDeployment.Result.ID == initialDeployment.Result.ID || ready.Result.State != string(runtimev1.DeploymentReady) ||
		f.runtime.DeployCalls != 2 || f.runtime.ReconcileCalls != 0 || f.runtime.LastExpectedRevision != 2 ||
		f.runtime.LastDeployRequest.Artifact.Digest != repairBuild.Artifact.Digest {
		t.Fatalf("repaired=%+v ready=%+v runtime deploys=%d reconciles=%d request=%+v", repairedDeployment, ready, f.runtime.DeployCalls, f.runtime.ReconcileCalls, f.runtime.LastDeployRequest)
	}

	audit, err := f.svc.AuditTrail(context.Background(), "tenant-acme", "task-booking")
	if err != nil {
		t.Fatal(err)
	}
	counts := map[agentv1.Tool]int{}
	for _, event := range audit {
		counts[event.Tool]++
	}
	if counts[agentv1.ToolApplyRepositoryPatch] != 2 || counts[agentv1.ToolRequestBuild] != 2 || counts[agentv1.ToolDeploy] != 2 || counts[agentv1.ToolGetDeployment] != 2 {
		t.Fatalf("repair audit counts=%v", counts)
	}
}
