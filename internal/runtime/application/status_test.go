package application_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/keir-research/ai-native-paas/internal/runtime/application"
	"github.com/keir-research/ai-native-paas/internal/runtime/domain"
	"github.com/keir-research/ai-native-paas/internal/runtime/gitops"
	"github.com/keir-research/ai-native-paas/internal/runtime/testkit"
	runtimev1 "github.com/keir-research/ai-native-paas/pkg/contracts/runtime/v1"
)

type statusFixture struct {
	*fixture
	observer   *testkit.Observer
	deployment runtimev1.DeploymentRef
	object     runtimev1.PaaSApp
}

func newStatusFixture(t *testing.T) *statusFixture {
	t.Helper()
	f := newFixture(t)
	observer := &testkit.Observer{}
	f.service.Observer = observer
	ref, err := f.service.Deploy(context.Background(), f.request("status-deploy"))
	if err != nil {
		t.Fatal(err)
	}
	snapshot := f.store.Snapshot()
	release := snapshot.Releases[ref.ReleaseID]
	deployment := snapshot.Deployments[ref.DeploymentID]
	placement := snapshot.Placements[deployment.PlacementID]
	cell := snapshot.Cells[placement.CellID]
	env := snapshot.Environments[deployment.EnvironmentID]
	app := snapshot.Applications[deployment.ApplicationID]
	bundle, err := (gitops.Renderer{}).Render(application.RenderInput{Application: app, Environment: env, Release: release, Placement: placement, Cell: cell})
	if err != nil {
		t.Fatal(err)
	}
	var object runtimev1.PaaSApp
	if err := json.Unmarshal(bundle.Files["paasapp.yaml"], &object); err != nil {
		t.Fatal(err)
	}
	object.Metadata.Generation = 3
	object.Metadata.UID = "uid-runtime-1"
	return &statusFixture{fixture: f, observer: observer, deployment: ref, object: object}
}
func (f *statusFixture) put(status runtimev1.PaaSAppStatus) {
	f.object.Status = status
	f.observer.Put(f.cell.ID, f.object)
}

func TestStatus_PaaSAppReadyMarksDeploymentReady(t *testing.T) {
	f := newStatusFixture(t)
	f.put(runtimev1.PaaSAppStatus{ObservedGeneration: f.object.Metadata.Generation, Phase: "Ready", ActiveReleaseID: f.deployment.ReleaseID, URL: "https://" + f.object.Spec.Route.GeneratedHostname, ReadyReplicas: 2})
	if err := f.service.ReconcileDeploymentStatus(context.Background(), f.deployment.DeploymentID); err != nil {
		t.Fatal(err)
	}
	status, err := f.service.Status(context.Background(), f.deployment.DeploymentID)
	if err != nil {
		t.Fatal(err)
	}
	if status.Phase != runtimev1.DeploymentReady || status.ActiveRelease != f.deployment.ReleaseID || status.ReadyReplicas != 2 || status.URL == "" {
		t.Fatalf("status=%+v", status)
	}
	snapshot := f.store.Snapshot()
	if snapshot.Releases[f.deployment.ReleaseID].State != runtimev1.ReleaseActive || snapshot.Environments[f.env.ID].ActiveReleaseID != f.deployment.ReleaseID {
		t.Fatalf("release=%+v env=%+v", snapshot.Releases[f.deployment.ReleaseID], snapshot.Environments[f.env.ID])
	}
}

func TestStatus_ArgoSyncedButAppNotReadyStaysDeploying(t *testing.T) {
	f := newStatusFixture(t)
	f.put(runtimev1.PaaSAppStatus{ObservedGeneration: f.object.Metadata.Generation, Phase: "Deploying", CandidateReleaseID: f.deployment.ReleaseID})
	if err := f.service.ReconcileDeploymentStatus(context.Background(), f.deployment.DeploymentID); err != nil {
		t.Fatal(err)
	}
	status, err := f.service.Status(context.Background(), f.deployment.DeploymentID)
	if err != nil {
		t.Fatal(err)
	}
	if status.Phase != runtimev1.DeploymentRollingOut || status.ActiveRelease != "" {
		t.Fatalf("status=%+v", status)
	}
}

func TestStatus_MissedWatchEventRecoveredByPeriodicRead(t *testing.T) {
	f := newStatusFixture(t)
	// No event is sent to the control plane. The periodic reconciler reads the
	// current object directly from the runtime observer and converges state.
	f.put(runtimev1.PaaSAppStatus{ObservedGeneration: f.object.Metadata.Generation, Phase: "Ready", ActiveReleaseID: f.deployment.ReleaseID, URL: "https://" + f.object.Spec.Route.GeneratedHostname, ReadyReplicas: 1})
	if err := f.service.ReconcileDeploymentStatus(context.Background(), f.deployment.DeploymentID); err != nil {
		t.Fatal(err)
	}
	status, _ := f.service.Status(context.Background(), f.deployment.DeploymentID)
	if status.Phase != runtimev1.DeploymentReady {
		t.Fatalf("status=%+v", status)
	}
}

func TestRuntime_UnknownObservedObjectIsQuarantined(t *testing.T) {
	f := newStatusFixture(t)
	err := f.service.ObserveRuntimeObject(context.Background(), application.ObservedObject{CellID: f.cell.ID, Namespace: "attacker", Kind: runtimev1.Kind, Name: "foreign", Labels: map[string]string{"platform.example.com/release-id": f.deployment.ReleaseID}})
	if err != nil {
		t.Fatal(err)
	}
	snapshot := f.store.Snapshot()
	if len(snapshot.Quarantine) != 1 {
		t.Fatalf("quarantine=%#v", snapshot.Quarantine)
	}
	for _, record := range snapshot.Quarantine {
		if !strings.Contains(record.Reason, "not owned") {
			t.Fatalf("record=%+v", record)
		}
	}
}

func TestStatus_WrongReleaseIdentityCannotActivateDeployment(t *testing.T) {
	f := newStatusFixture(t)
	f.object.Spec.Identity.ReleaseID = "rel-attacker"
	f.put(runtimev1.PaaSAppStatus{ObservedGeneration: f.object.Metadata.Generation, Phase: "Ready", ActiveReleaseID: f.deployment.ReleaseID, URL: "https://example.test", ReadyReplicas: 1})
	err := f.service.ReconcileDeploymentStatus(context.Background(), f.deployment.DeploymentID)
	if !domain.HasCode(err, domain.CodeConflict) {
		t.Fatalf("err=%v", err)
	}
	status, _ := f.service.Status(context.Background(), f.deployment.DeploymentID)
	if status.Phase == runtimev1.DeploymentReady {
		t.Fatalf("forged object activated deployment: %+v", status)
	}
}

func TestStatus_WrongImageDigestCannotActivateDeployment(t *testing.T) {
	f := newStatusFixture(t)
	f.object.Spec.Image.Digest = "sha256:" + strings.Repeat("b", 64)
	f.put(runtimev1.PaaSAppStatus{ObservedGeneration: f.object.Metadata.Generation, Phase: "Ready", ActiveReleaseID: f.deployment.ReleaseID, URL: "https://example.test", ReadyReplicas: 1})
	err := f.service.ReconcileDeploymentStatus(context.Background(), f.deployment.DeploymentID)
	if !domain.HasCode(err, domain.CodeConflict) {
		t.Fatalf("err=%v", err)
	}
}

func TestStatus_StaleObservedGenerationDoesNotActivateDeployment(t *testing.T) {
	f := newStatusFixture(t)
	f.put(runtimev1.PaaSAppStatus{ObservedGeneration: f.object.Metadata.Generation - 1, Phase: "Ready", ActiveReleaseID: f.deployment.ReleaseID, URL: "https://example.test", ReadyReplicas: 1})
	if err := f.service.ReconcileDeploymentStatus(context.Background(), f.deployment.DeploymentID); err != nil {
		t.Fatal(err)
	}
	status, _ := f.service.Status(context.Background(), f.deployment.DeploymentID)
	if status.Phase != runtimev1.DeploymentRollingOut || status.ActiveRelease != "" {
		t.Fatalf("stale status activated release: %+v", status)
	}
}

func TestStatus_DegradedThenReadyRecoversSameRelease(t *testing.T) {
	f := newStatusFixture(t)
	f.put(runtimev1.PaaSAppStatus{ObservedGeneration: f.object.Metadata.Generation, Phase: "Degraded", CandidateReleaseID: f.deployment.ReleaseID, Conditions: []runtimev1.Condition{{Type: "Ready", Status: "False", Message: "temporary timeout"}}})
	if err := f.service.ReconcileDeploymentStatus(context.Background(), f.deployment.DeploymentID); err != nil {
		t.Fatal(err)
	}
	first := f.store.Snapshot()
	if first.Deployments[f.deployment.DeploymentID].Phase != runtimev1.DeploymentDegraded || first.Releases[f.deployment.ReleaseID].State != runtimev1.ReleaseDeploying {
		t.Fatalf("deployment=%+v release=%+v", first.Deployments[f.deployment.DeploymentID], first.Releases[f.deployment.ReleaseID])
	}
	f.put(runtimev1.PaaSAppStatus{ObservedGeneration: f.object.Metadata.Generation, Phase: "Ready", ActiveReleaseID: f.deployment.ReleaseID, URL: "https://" + f.object.Spec.Route.GeneratedHostname, ReadyReplicas: 1})
	if err := f.service.ReconcileDeploymentStatus(context.Background(), f.deployment.DeploymentID); err != nil {
		t.Fatal(err)
	}
	second := f.store.Snapshot()
	if second.Deployments[f.deployment.DeploymentID].Phase != runtimev1.DeploymentReady || second.Releases[f.deployment.ReleaseID].State != runtimev1.ReleaseActive {
		t.Fatalf("deployment=%+v release=%+v", second.Deployments[f.deployment.DeploymentID], second.Releases[f.deployment.ReleaseID])
	}
}

func TestStatus_MissingPaaSAppMarksArgoSyncing(t *testing.T) {
	f := newStatusFixture(t)
	if err := f.service.ReconcileDeploymentStatus(context.Background(), f.deployment.DeploymentID); err != nil {
		t.Fatal(err)
	}
	status, _ := f.service.Status(context.Background(), f.deployment.DeploymentID)
	if status.Phase != runtimev1.DeploymentArgoSyncing {
		t.Fatalf("status=%+v", status)
	}
}
