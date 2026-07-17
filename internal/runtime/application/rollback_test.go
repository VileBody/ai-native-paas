package application_test

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/keir-research/ai-native-paas/internal/runtime/application"
	"github.com/keir-research/ai-native-paas/internal/runtime/domain"
)

func deployRollbackTarget(t *testing.T, f *fixture) string {
	t.Helper()
	ref, err := f.service.Deploy(context.Background(), f.request("rollback-target"))
	if err != nil {
		t.Fatal(err)
	}
	return ref.ReleaseID
}

func TestRollback_ReusesPreviousArtifactDigest(t *testing.T) {
	f := newFixture(t)
	targetID := deployRollbackTarget(t, f)
	ref, err := f.service.RollbackWithRequest(context.Background(), application.RollbackRequest{TenantID: f.app.TenantID, EnvironmentID: f.env.ID, TargetReleaseID: targetID, ActorID: "user-1", IdempotencyKey: "rollback-reuse"})
	if err != nil {
		t.Fatal(err)
	}
	snapshot := f.store.Snapshot()
	target, candidate := snapshot.Releases[targetID], snapshot.Releases[ref.ReleaseID]
	if candidate.ID == target.ID || candidate.Artifact.Digest != target.Artifact.Digest || candidate.Artifact.Repository != target.Artifact.Repository {
		t.Fatalf("target=%+v candidate=%+v", target, candidate)
	}
}

func TestRollback_DoesNotInvokeBuildPort(t *testing.T) {
	f := newFixture(t)
	// Runtime Delivery intentionally has no build dependency. This architecture
	// assertion makes a future accidental source-to-image call a test failure.
	typ := reflect.TypeOf(f.service)
	for i := 0; i < typ.NumField(); i++ {
		if strings.Contains(strings.ToLower(typ.Field(i).Name), "build") {
			t.Fatalf("runtime service gained a build dependency: %s", typ.Field(i).Name)
		}
	}
	targetID := deployRollbackTarget(t, f)
	before := f.policy.CallCount()
	ref, err := f.service.RollbackWithRequest(context.Background(), application.RollbackRequest{TenantID: f.app.TenantID, EnvironmentID: f.env.ID, TargetReleaseID: targetID, ActorID: "user-1", IdempotencyKey: "rollback-no-build"})
	if err != nil {
		t.Fatal(err)
	}
	after := f.policy.CallCount()
	if after != before+1 {
		t.Fatalf("rollback should only re-check artifact policy: before=%d after=%d", before, after)
	}
	snapshot := f.store.Snapshot()
	if snapshot.Releases[ref.ReleaseID].Artifact != snapshot.Releases[targetID].Artifact {
		t.Fatal("rollback did not reuse immutable artifact")
	}
}

func TestRollback_CreatesNewAuditableRelease(t *testing.T) {
	f := newFixture(t)
	targetID := deployRollbackTarget(t, f)
	ref, err := f.service.RollbackWithRequest(context.Background(), application.RollbackRequest{TenantID: f.app.TenantID, EnvironmentID: f.env.ID, TargetReleaseID: targetID, ActorID: "user-1", IdempotencyKey: "rollback-audit"})
	if err != nil {
		t.Fatal(err)
	}
	snapshot := f.store.Snapshot()
	candidate := snapshot.Releases[ref.ReleaseID]
	if candidate.ID == targetID || candidate.RollbackOf != targetID {
		t.Fatalf("candidate=%+v", candidate)
	}
	found := false
	for _, event := range snapshot.Audit {
		if event.Action == "runtime.release.rollback" && event.ResourceID == candidate.ID && event.ActorID == "user-1" {
			found = true
		}
	}
	if !found {
		t.Fatalf("rollback audit missing: %#v", snapshot.Audit)
	}
}

func TestRollback_RejectsArtifactNoLongerAllowedByCriticalPolicyUnlessOverride(t *testing.T) {
	f := newFixture(t)
	targetID := deployRollbackTarget(t, f)
	f.policy.Allowed = false
	_, err := f.service.RollbackWithRequest(context.Background(), application.RollbackRequest{TenantID: f.app.TenantID, EnvironmentID: f.env.ID, TargetReleaseID: targetID, ActorID: "user-1", IdempotencyKey: "rollback-denied"})
	if !domain.HasCode(err, domain.CodePolicyRejected) {
		t.Fatalf("err=%v", err)
	}
	ref, err := f.service.RollbackWithRequest(context.Background(), application.RollbackRequest{TenantID: f.app.TenantID, EnvironmentID: f.env.ID, TargetReleaseID: targetID, ActorID: "admin-1", IdempotencyKey: "rollback-override", CriticalOverride: true})
	if err != nil {
		t.Fatal(err)
	}
	candidate := f.store.Snapshot().Releases[ref.ReleaseID]
	if !candidate.PolicyOverride || candidate.RollbackOf != targetID {
		t.Fatalf("candidate=%+v", candidate)
	}
}

func TestRollbackByDeploymentResolvesAuthoritativeEnvironmentAndRevision(t *testing.T) {
	f := newFixture(t)
	targetID := deployRollbackTarget(t, f)
	snapshot := f.store.Snapshot()
	var deploymentID string
	for _, deployment := range snapshot.Deployments {
		if deployment.ReleaseID == targetID {
			deploymentID = deployment.ID
			break
		}
	}
	if deploymentID == "" {
		t.Fatal("target deployment missing")
	}
	environment := snapshot.Environments[f.env.ID]
	ref, err := f.service.RollbackDeploymentWithRequest(context.Background(), deploymentID, application.RollbackRequest{
		TenantID: f.app.TenantID, TargetReleaseID: targetID, ActorID: "agent-api", IdempotencyKey: "rollback-by-deployment",
		ExpectedEnvironmentRevision: environment.Version,
	})
	if err != nil {
		t.Fatal(err)
	}
	if ref.DeploymentID == "" || f.store.Snapshot().Releases[ref.ReleaseID].RollbackOf != targetID {
		t.Fatalf("rollback ref=%+v", ref)
	}

	_, err = f.service.RollbackDeploymentWithRequest(context.Background(), deploymentID, application.RollbackRequest{
		TenantID: f.app.TenantID, TargetReleaseID: targetID, ActorID: "agent-api", IdempotencyKey: "rollback-stale-revision",
		ExpectedEnvironmentRevision: environment.Version + 100,
	})
	if !domain.HasCode(err, domain.CodeStaleVersion) {
		t.Fatalf("stale rollback err=%v", err)
	}
}
