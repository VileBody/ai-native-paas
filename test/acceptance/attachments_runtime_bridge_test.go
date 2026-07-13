package acceptance_test

import (
	"context"
	"strconv"
	"testing"
	"time"

	runtimebridge "github.com/keir-research/ai-native-paas/adapters/attachmentsruntime"
	attachmentsapp "github.com/keir-research/ai-native-paas/internal/attachments/application"
	attachmentsmemory "github.com/keir-research/ai-native-paas/internal/attachments/memory"
	attachmentstestkit "github.com/keir-research/ai-native-paas/internal/attachments/testkit"
	runtimeapp "github.com/keir-research/ai-native-paas/internal/runtime/application"
	"github.com/keir-research/ai-native-paas/internal/runtime/gitops"
	runtimememory "github.com/keir-research/ai-native-paas/internal/runtime/memory"
	runtimetestkit "github.com/keir-research/ai-native-paas/internal/runtime/testkit"
	attachmentsv1 "github.com/keir-research/ai-native-paas/pkg/contracts/attachments/v1"
	runtimev1 "github.com/keir-research/ai-native-paas/pkg/contracts/runtime/v1"
)

type attachmentRuntimeFixture struct {
	runtime      *runtimeapp.Service
	runtimeStore *runtimememory.Store
	attachments  *attachmentsapp.Service
	tenantID     string
	appID        string
	envID        string
	initialID    string
}

func newAttachmentRuntimeFixture(t *testing.T) *attachmentRuntimeFixture {
	t.Helper()
	ctx := context.Background()
	runtimeClock := &runtimetestkit.Clock{T: time.Date(2026, 7, 13, 10, 0, 0, 0, time.UTC)}
	runtimeStore := runtimememory.New()
	runtimeService := &runtimeapp.Service{
		Store: runtimeStore, Artifacts: &runtimetestkit.ArtifactPolicy{Allowed: true}, Renderer: gitops.Renderer{}, GitOps: &acceptanceGitOps{},
		Units: runtimeapp.StaticUnitCatalog{"u1": 1}, Clock: runtimeClock, IDs: &runtimetestkit.IDs{}, Scheduler: runtimeapp.DeterministicScheduler{},
	}
	if _, err := runtimeService.RegisterCell(ctx, runtimeapp.RegisterCellRequest{
		ID: "cell-a", Region: "eu1", Isolation: []runtimev1.IsolationClass{runtimev1.IsolationSandboxed}, CapacityUnits: 100,
		GitOpsRepository: "https://git.example.invalid/runtime.git", ClusterServer: "https://kubernetes.default.svc",
		ArgoProject: "runtime-cell", IngressDomain: "apps.eu1.example.invalid",
	}); err != nil {
		t.Fatal(err)
	}
	app, env, err := runtimeService.CreateApplication(ctx, runtimeapp.CreateApplicationRequest{
		TenantID: "tenant-1", ProjectID: "project-1", Name: "booking", ActorID: "user-1", IdempotencyKey: "create-app",
	})
	if err != nil {
		t.Fatal(err)
	}
	initial, err := runtimeService.Deploy(ctx, runtimev1.DeployRequest{
		TenantID: app.TenantID, ApplicationID: app.ID, EnvironmentID: env.ID, Artifact: runtimetestkit.Artifact("a"),
		Configuration: runtimetestkit.Config(), IdempotencyKey: "initial-deploy", ActorID: "user-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	markRuntimeReleaseActive(t, runtimeService, env.ID, initial.ReleaseID)

	attachmentsClock := attachmentstestkit.NewClock()
	attachments := &attachmentsapp.Service{
		Store: attachmentsmemory.New(),
		Environments: &attachmentstestkit.Environments{Values: map[string]attachmentsapp.EnvironmentRef{
			env.ID: {TenantID: app.TenantID, ApplicationID: app.ID, EnvironmentID: env.ID, Ready: true},
		}},
		Secrets: attachmentstestkit.NewVault(), Runtime: &runtimebridge.Publisher{Runtime: runtimeService},
		Clock: attachmentsClock, IDs: &attachmentsapp.SequentialIDs{},
	}
	return &attachmentRuntimeFixture{runtime: runtimeService, runtimeStore: runtimeStore, attachments: attachments, tenantID: app.TenantID, appID: app.ID, envID: env.ID, initialID: initial.ReleaseID}
}

func markRuntimeReleaseActive(t *testing.T, runtimeService *runtimeapp.Service, environmentID, releaseID string) {
	t.Helper()
	now := runtimeService.Clock.Now()
	if err := runtimeService.Store.Transact(context.Background(), func(tx runtimeapp.Tx) error {
		environment, _ := tx.GetEnvironment(environmentID)
		if environment.ActiveReleaseID != "" && environment.ActiveReleaseID != releaseID {
			previous, ok := tx.GetRelease(environment.ActiveReleaseID)
			if ok && previous.State == runtimev1.ReleaseActive {
				expected := previous.Version
				if err := previous.Transition(runtimev1.ReleaseSuperseded, now); err != nil {
					return err
				}
				if err := tx.UpdateRelease(previous, expected); err != nil {
					return err
				}
			}
		}
		release, _ := tx.GetRelease(releaseID)
		expectedRelease := release.Version
		if release.State == runtimev1.ReleaseCommittedToGitOps {
			if err := release.Transition(runtimev1.ReleaseDeploying, now); err != nil {
				return err
			}
		}
		if release.State == runtimev1.ReleaseDeploying {
			if err := release.Transition(runtimev1.ReleaseActive, now); err != nil {
				return err
			}
		}
		if err := tx.UpdateRelease(release, expectedRelease); err != nil {
			return err
		}
		expectedEnvironment := environment.Version
		if err := environment.ActivateRelease(releaseID, now); err != nil {
			return err
		}
		return tx.UpdateEnvironment(environment, expectedEnvironment)
	}); err != nil {
		t.Fatal(err)
	}
}

func (f *attachmentRuntimeFixture) setSecret(t *testing.T, value, key string) attachmentsv1.AttachmentSnapshotRef {
	t.Helper()
	_, snapshot, err := f.attachments.SetSecret(context.Background(), attachmentsapp.SetSecretRequest{
		TenantID: f.tenantID, ApplicationID: f.appID, EnvironmentID: f.envID,
		Name: "API_TOKEN", Scope: attachmentsv1.SecretScopeRuntime, Phase: attachmentsv1.SecretPhaseRuntime,
		Value: []byte(value), ActorID: "user-1", IdempotencyKey: key,
	})
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func (f *attachmentRuntimeFixture) releaseForSnapshot(t *testing.T, snapshot attachmentsv1.AttachmentSnapshotRef) string {
	t.Helper()
	want := snapshot.SnapshotID + ":v" + strconv.FormatInt(snapshot.Version, 10)
	var releaseID string
	if err := f.runtimeStore.Transact(context.Background(), func(tx runtimeapp.Tx) error {
		for _, release := range tx.ListReleasesByEnvironment(f.envID) {
			if release.Configuration.AttachmentSnapshotRef == want {
				releaseID = release.ID
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if releaseID == "" {
		t.Fatalf("no runtime release for attachment snapshot %s", want)
	}
	return releaseID
}

func TestAttachments_RuntimeReleaseUsesSameImageDigestAndNewSnapshot(t *testing.T) {
	f := newAttachmentRuntimeFixture(t)
	snapshot := f.setSecret(t, "one", "secret-1")
	releaseID := f.releaseForSnapshot(t, snapshot)
	if err := f.runtimeStore.Transact(context.Background(), func(tx runtimeapp.Tx) error {
		initial, _ := tx.GetRelease(f.initialID)
		release, _ := tx.GetRelease(releaseID)
		if initial.Artifact.Digest != release.Artifact.Digest || initial.Configuration.AttachmentSnapshotRef == release.Configuration.AttachmentSnapshotRef {
			t.Fatalf("initial=%+v release=%+v", initial, release)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestAttachments_RuntimeRollbackRestoresMatchingSnapshot(t *testing.T) {
	f := newAttachmentRuntimeFixture(t)
	firstSnapshot := f.setSecret(t, "one", "secret-1")
	firstReleaseID := f.releaseForSnapshot(t, firstSnapshot)
	markRuntimeReleaseActive(t, f.runtime, f.envID, firstReleaseID)
	secondSnapshot := f.setSecret(t, "two", "secret-2")
	_ = f.releaseForSnapshot(t, secondSnapshot)

	rollback, err := f.runtime.RollbackWithRequest(context.Background(), runtimeapp.RollbackRequest{
		TenantID: f.tenantID, EnvironmentID: f.envID, TargetReleaseID: firstReleaseID,
		ActorID: "user-1", IdempotencyKey: "rollback-snapshot-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.runtimeStore.Transact(context.Background(), func(tx runtimeapp.Tx) error {
		target, _ := tx.GetRelease(firstReleaseID)
		candidate, _ := tx.GetRelease(rollback.ReleaseID)
		if candidate.Configuration.AttachmentSnapshotRef != target.Configuration.AttachmentSnapshotRef {
			t.Fatalf("rollback snapshot=%q target=%q", candidate.Configuration.AttachmentSnapshotRef, target.Configuration.AttachmentSnapshotRef)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
