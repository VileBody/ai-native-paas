package acceptance_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/keir-research/ai-native-paas/internal/runtime/application"
	"github.com/keir-research/ai-native-paas/internal/runtime/gitops"
	"github.com/keir-research/ai-native-paas/internal/runtime/kube"
	"github.com/keir-research/ai-native-paas/internal/runtime/memory"
	"github.com/keir-research/ai-native-paas/internal/runtime/operator"
	"github.com/keir-research/ai-native-paas/internal/runtime/testkit"
	runtimev1 "github.com/keir-research/ai-native-paas/pkg/contracts/runtime/v1"
)

type acceptanceGitOps struct {
	bundles map[string]application.RenderedBundle
}

func (g *acceptanceGitOps) Commit(_ context.Context, req application.CommitRequest) (application.CommitResult, error) {
	if g.bundles == nil {
		g.bundles = map[string]application.RenderedBundle{}
	}
	if existing, ok := g.bundles[req.Bundle.ReleaseID]; ok {
		return application.CommitResult{CommitSHA: strings.Repeat("a", 40), Path: existing.Path, ManifestHash: existing.ManifestHash}, nil
	}
	g.bundles[req.Bundle.ReleaseID] = req.Bundle
	return application.CommitResult{CommitSHA: strings.Repeat("a", 40), Path: req.Bundle.Path, ManifestHash: req.Bundle.ManifestHash}, nil
}
func (g *acceptanceGitOps) FindByRelease(_ context.Context, _ string, releaseID string) (application.CommitResult, bool, error) {
	bundle, ok := g.bundles[releaseID]
	return application.CommitResult{CommitSHA: strings.Repeat("a", 40), Path: bundle.Path, ManifestHash: bundle.ManifestHash}, ok, nil
}
func (g *acceptanceGitOps) app(t *testing.T, releaseID string) runtimev1.PaaSApp {
	t.Helper()
	bundle, ok := g.bundles[releaseID]
	if !ok {
		t.Fatalf("missing GitOps bundle for release %s", releaseID)
	}
	var app runtimev1.PaaSApp
	if err := json.Unmarshal(bundle.Files["paasapp.yaml"], &app); err != nil {
		t.Fatal(err)
	}
	return app
}

func markAllDeploymentsReady(t *testing.T, client *kube.MemoryClient, namespace string) {
	t.Helper()
	deployments, err := client.ListDeployments(context.Background(), namespace, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(deployments) == 0 {
		t.Fatal("operator created no deployments")
	}
	for _, deployment := range deployments {
		replicas := 1
		if deployment.Spec.Replicas != nil {
			replicas = *deployment.Spec.Replicas
		}
		client.SetDeploymentStatus(namespace, deployment.Metadata.Name, kube.DeploymentStatus{ReadyReplicas: replicas})
	}
}

func TestRuntimeDelivery_CommitToReadyAndRollbackWithoutLiveKubernetes(t *testing.T) {
	ctx := context.Background()
	clock := &testkit.Clock{T: time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)}
	store := memory.New()
	git := &acceptanceGitOps{}
	observer := &testkit.Observer{Objects: map[string]runtimev1.PaaSApp{}}
	service := application.Service{
		Store: store, Artifacts: &testkit.ArtifactPolicy{Allowed: true}, Renderer: gitops.Renderer{}, GitOps: git, Observer: observer,
		Units: application.StaticUnitCatalog{"u1": 1}, Clock: clock, IDs: &testkit.IDs{}, Scheduler: application.DeterministicScheduler{},
	}
	cell, err := service.RegisterCell(ctx, application.RegisterCellRequest{ID: "cell-a", Region: "eu1", Isolation: []runtimev1.IsolationClass{runtimev1.IsolationSandboxed}, CapacityUnits: 100, GitOpsRepository: "https://git.example.invalid/runtime.git", ClusterServer: "https://kubernetes.default.svc", ArgoProject: "runtime-cell", IngressDomain: "apps.eu1.example.invalid"})
	if err != nil {
		t.Fatal(err)
	}
	appRecord, env, err := service.CreateApplication(ctx, application.CreateApplicationRequest{TenantID: "tenant-1", ProjectID: "project-1", Name: "booking", ActorID: "user-1", IdempotencyKey: "create-app"})
	if err != nil {
		t.Fatal(err)
	}

	first, err := service.Deploy(ctx, runtimev1.DeployRequest{TenantID: appRecord.TenantID, ApplicationID: appRecord.ID, EnvironmentID: env.ID, Artifact: testkit.Artifact("a"), Configuration: testkit.Config(), IdempotencyKey: "deploy-a", ActorID: "user-1"})
	if err != nil {
		t.Fatal(err)
	}
	if first.Phase != runtimev1.DeploymentGitCommitted {
		t.Fatalf("first deployment phase=%s", first.Phase)
	}

	client := kube.NewMemoryClient()
	reconciler := operator.Reconciler{Client: client, Clock: clock, Units: map[string]runtimev1.ResourceQuantity{"u1": {CPU: "250m", Memory: "512Mi", EphemeralStorage: "1Gi"}}}
	firstApp := git.app(t, first.ReleaseID)
	firstApp.Metadata.Generation = 1
	firstApp.Metadata.UID = "uid-booking"
	client.PutPaaSApp(firstApp)
	if err := reconciler.Reconcile(ctx, firstApp); err != nil {
		t.Fatal(err)
	}
	observedFirst, ok := client.GetPaaSAppObject(firstApp.Metadata.Namespace, firstApp.Metadata.Name)
	if !ok || observedFirst.Status.Phase != "Deploying" {
		t.Fatalf("first reconcile status=%+v", observedFirst.Status)
	}
	markAllDeploymentsReady(t, client, firstApp.Metadata.Namespace)
	if err := reconciler.Reconcile(ctx, observedFirst); err != nil {
		t.Fatal(err)
	}
	readyFirst, _ := client.GetPaaSAppObject(firstApp.Metadata.Namespace, firstApp.Metadata.Name)
	if readyFirst.Status.Phase != "Ready" || readyFirst.Status.ActiveReleaseID != first.ReleaseID {
		t.Fatalf("first ready status=%+v", readyFirst.Status)
	}
	observer.Put(cell.ID, readyFirst)
	if err := service.ReconcileDeploymentStatus(ctx, first.DeploymentID); err != nil {
		t.Fatal(err)
	}
	firstStatus, err := service.StatusForTenant(ctx, appRecord.TenantID, first.DeploymentID)
	if err != nil || firstStatus.Phase != runtimev1.DeploymentReady || firstStatus.ActiveRelease != first.ReleaseID {
		t.Fatalf("first status=%+v err=%v", firstStatus, err)
	}

	second, err := service.Deploy(ctx, runtimev1.DeployRequest{TenantID: appRecord.TenantID, ApplicationID: appRecord.ID, EnvironmentID: env.ID, Artifact: testkit.Artifact("b"), Configuration: testkit.Config(), IdempotencyKey: "deploy-b", ActorID: "user-1"})
	if err != nil {
		t.Fatal(err)
	}
	secondApp := git.app(t, second.ReleaseID)
	secondApp.Metadata.Generation = 2
	secondApp.Metadata.UID = firstApp.Metadata.UID
	secondApp.Status = readyFirst.Status
	client.PutPaaSApp(secondApp)
	if err := reconciler.Reconcile(ctx, secondApp); err != nil {
		t.Fatal(err)
	}
	progressingSecond, _ := client.GetPaaSAppObject(secondApp.Metadata.Namespace, secondApp.Metadata.Name)
	markAllDeploymentsReady(t, client, secondApp.Metadata.Namespace)
	if err := reconciler.Reconcile(ctx, progressingSecond); err != nil {
		t.Fatal(err)
	}
	readySecond, _ := client.GetPaaSAppObject(secondApp.Metadata.Namespace, secondApp.Metadata.Name)
	observer.Put(cell.ID, readySecond)
	if err := service.ReconcileDeploymentStatus(ctx, second.DeploymentID); err != nil {
		t.Fatal(err)
	}
	secondStatus, err := service.StatusForTenant(ctx, appRecord.TenantID, second.DeploymentID)
	if err != nil || secondStatus.Phase != runtimev1.DeploymentReady || secondStatus.ActiveRelease != second.ReleaseID {
		t.Fatalf("second status=%+v err=%v", secondStatus, err)
	}

	rollback, err := service.RollbackWithRequest(ctx, application.RollbackRequest{TenantID: appRecord.TenantID, EnvironmentID: env.ID, TargetReleaseID: first.ReleaseID, ActorID: "user-1", IdempotencyKey: "rollback-a"})
	if err != nil {
		t.Fatal(err)
	}
	rollbackApp := git.app(t, rollback.ReleaseID)
	if rollbackApp.Spec.Image.Digest != firstApp.Spec.Image.Digest || rollbackApp.Spec.Image.Repository != firstApp.Spec.Image.Repository {
		t.Fatalf("rollback artifact=%+v first=%+v", rollbackApp.Spec.Image, firstApp.Spec.Image)
	}
	if rollback.ReleaseID == first.ReleaseID {
		t.Fatal("rollback must create an auditable new release identity")
	}
}
