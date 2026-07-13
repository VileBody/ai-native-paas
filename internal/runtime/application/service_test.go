package application_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/keir-research/ai-native-paas/internal/runtime/application"
	"github.com/keir-research/ai-native-paas/internal/runtime/domain"
	"github.com/keir-research/ai-native-paas/internal/runtime/gitops"
	"github.com/keir-research/ai-native-paas/internal/runtime/memory"
	"github.com/keir-research/ai-native-paas/internal/runtime/testkit"
	runtimev1 "github.com/keir-research/ai-native-paas/pkg/contracts/runtime/v1"
)

type fakeGitOps struct {
	commits map[string]application.CommitResult
	calls   int
}

func (f *fakeGitOps) Commit(_ context.Context, req application.CommitRequest) (application.CommitResult, error) {
	if f.commits == nil {
		f.commits = map[string]application.CommitResult{}
	}
	if v, ok := f.commits[req.Bundle.ReleaseID]; ok {
		return v, nil
	}
	f.calls++
	v := application.CommitResult{CommitSHA: "0123456789abcdef0123456789abcdef01234567", Path: req.Bundle.Path, ManifestHash: req.Bundle.ManifestHash}
	f.commits[req.Bundle.ReleaseID] = v
	return v, nil
}
func (f *fakeGitOps) FindByRelease(_ context.Context, _ string, release string) (application.CommitResult, bool, error) {
	v, ok := f.commits[release]
	return v, ok, nil
}

type fixture struct {
	service application.Service
	store   *memory.Store
	policy  *testkit.ArtifactPolicy
	clock   *testkit.Clock
	ids     *testkit.IDs
	git     *fakeGitOps
	app     domain.Application
	env     domain.Environment
	cell    domain.RuntimeCell
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	ctx := context.Background()
	store := memory.New()
	clock := &testkit.Clock{T: time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)}
	ids := &testkit.IDs{}
	policy := &testkit.ArtifactPolicy{Allowed: true}
	git := &fakeGitOps{}
	service := application.Service{Store: store, Artifacts: policy, Renderer: gitops.Renderer{}, GitOps: git, Units: application.StaticUnitCatalog{"u1": 1, "u2": 2}, Clock: clock, IDs: ids, Scheduler: application.DeterministicScheduler{}}
	cell, err := service.RegisterCell(ctx, application.RegisterCellRequest{ID: "cell-a", Region: "eu1", Isolation: []runtimev1.IsolationClass{runtimev1.IsolationSandboxed}, CapacityUnits: 100, GitOpsRepository: "https://git.test/runtime-cell-a.git", ClusterServer: "https://kubernetes.default.svc", ArgoProject: "runtime-cell", IngressDomain: "apps.eu1.test"})
	if err != nil {
		t.Fatal(err)
	}
	app, env, err := service.CreateApplication(ctx, application.CreateApplicationRequest{TenantID: "tenant-1", ProjectID: "project-1", Name: "Booking", ActorID: "user-1", IdempotencyKey: "create-app"})
	if err != nil {
		t.Fatal(err)
	}
	return &fixture{service: service, store: store, policy: policy, clock: clock, ids: ids, git: git, app: app, env: env, cell: cell}
}
func (f *fixture) request(key string) runtimev1.DeployRequest {
	return runtimev1.DeployRequest{TenantID: f.app.TenantID, ApplicationID: f.app.ID, EnvironmentID: f.env.ID, Artifact: testkit.Artifact("a"), Configuration: testkit.Config(), IdempotencyKey: key, ActorID: "user-1"}
}

func TestApplication_CreateBelongsToTenantProject(t *testing.T) {
	f := newFixture(t)
	if f.app.TenantID != "tenant-1" || f.app.ProjectID != "project-1" || f.app.Name != "booking" {
		t.Fatalf("app=%+v", f.app)
	}
	if f.env.ApplicationID != f.app.ID || f.env.TenantID != f.app.TenantID {
		t.Fatalf("env=%+v", f.env)
	}
}
func TestEnvironment_DefaultProductionIsUnique(t *testing.T) {
	f := newFixture(t)
	_, err := f.service.CreateEnvironment(context.Background(), application.CreateEnvironmentRequest{TenantID: f.app.TenantID, ApplicationID: f.app.ID, Name: "second", Default: true, ActorID: "user-1", IdempotencyKey: "env-default"})
	if !domain.HasCode(err, domain.CodeConflict) {
		t.Fatalf("err=%v", err)
	}
}
func TestEnvironment_NameIsUniqueWithinApplication(t *testing.T) {
	f := newFixture(t)
	_, err := f.service.CreateEnvironment(context.Background(), application.CreateEnvironmentRequest{TenantID: f.app.TenantID, ApplicationID: f.app.ID, Name: "production", ActorID: "user-1", IdempotencyKey: "env-name"})
	if !domain.HasCode(err, domain.CodeConflict) {
		t.Fatalf("err=%v", err)
	}
}
func TestEnvironment_NamespaceNameIsDeterministic(t *testing.T) {
	a, err := domain.NewEnvironment("env-1", "tenant-1", "app-1", "Preview", false, time.Unix(1, 0))
	if err != nil {
		t.Fatal(err)
	}
	b, err := domain.NewEnvironment("env-2", "tenant-1", "app-1", "preview", false, time.Unix(2, 0))
	if err != nil {
		t.Fatal(err)
	}
	if a.Namespace != b.Namespace || a.Namespace == "" {
		t.Fatalf("%q != %q", a.Namespace, b.Namespace)
	}
}
func TestEnvironment_CannotChangeTenant(t *testing.T) {
	f := newFixture(t)
	err := f.store.Transact(context.Background(), func(tx application.Tx) error {
		env, ok := tx.GetEnvironment(f.env.ID)
		if !ok {
			return errors.New("missing")
		}
		expected := env.Version
		env.TenantID = "tenant-2"
		env.Version++
		return tx.UpdateEnvironment(env, expected)
	})
	if !domain.HasCode(err, domain.CodeConflict) {
		t.Fatalf("err=%v", err)
	}
}
func TestRelease_RejectsNonReleasableArtifact(t *testing.T) {
	f := newFixture(t)
	f.policy.Allowed = false
	_, _, err := f.service.CreateRelease(context.Background(), f.request("release-reject"))
	if !domain.HasCode(err, domain.CodePolicyRejected) {
		t.Fatalf("err=%v", err)
	}
}
func TestRelease_StoresExactDigest(t *testing.T) {
	f := newFixture(t)
	req := f.request("release-digest")
	release, _, err := f.service.CreateRelease(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if release.Artifact.Digest != req.Artifact.Digest || release.Artifact.Repository != req.Artifact.Repository {
		t.Fatalf("artifact=%+v", release.Artifact)
	}
}
func TestRelease_SameArtifactAndConfigIsIdempotent(t *testing.T) {
	f := newFixture(t)
	a, pa, err := f.service.CreateRelease(context.Background(), f.request("release-a"))
	if err != nil {
		t.Fatal(err)
	}
	b, pb, err := f.service.CreateRelease(context.Background(), f.request("release-b"))
	if err != nil {
		t.Fatal(err)
	}
	if a.ID != b.ID || pa.ID != pb.ID {
		t.Fatalf("release %s/%s placement %s/%s", a.ID, b.ID, pa.ID, pb.ID)
	}
}
func TestRelease_ConfigChangeCreatesNewRelease(t *testing.T) {
	f := newFixture(t)
	a, _, err := f.service.CreateRelease(context.Background(), f.request("release-a"))
	if err != nil {
		t.Fatal(err)
	}
	req := f.request("release-b")
	p := req.Configuration.Processes["web"]
	p.MaxReplicas = 4
	req.Configuration.Processes["web"] = p
	b, _, err := f.service.CreateRelease(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if a.ID == b.ID || a.Identity == b.Identity {
		t.Fatalf("same release: %+v %+v", a, b)
	}
}
func TestRelease_OnlyOneActiveReleasePerEnvironment(t *testing.T) {
	f := newFixture(t)
	a, _, err := f.service.CreateRelease(context.Background(), f.request("release-a"))
	if err != nil {
		t.Fatal(err)
	}
	breq := f.request("release-b")
	p := breq.Configuration.Processes["web"]
	p.MaxReplicas = 4
	breq.Configuration.Processes["web"] = p
	b, _, err := f.service.CreateRelease(context.Background(), breq)
	if err != nil {
		t.Fatal(err)
	}
	err = f.store.Transact(context.Background(), func(tx application.Tx) error {
		av, _ := tx.GetRelease(a.ID)
		expected := av.Version
		_ = av.Transition(runtimev1.ReleaseCommittedToGitOps, f.clock.Now())
		_ = av.Transition(runtimev1.ReleaseDeploying, f.clock.Now())
		_ = av.Transition(runtimev1.ReleaseActive, f.clock.Now())
		if err := tx.UpdateRelease(av, expected); err != nil {
			return err
		}
		bv, _ := tx.GetRelease(b.ID)
		expected = bv.Version
		_ = bv.Transition(runtimev1.ReleaseCommittedToGitOps, f.clock.Now())
		_ = bv.Transition(runtimev1.ReleaseDeploying, f.clock.Now())
		_ = bv.Transition(runtimev1.ReleaseActive, f.clock.Now())
		return tx.UpdateRelease(bv, expected)
	})
	if !domain.HasCode(err, domain.CodeConflict) {
		t.Fatalf("err=%v", err)
	}
}
func TestPlacement_SelectsRegionAndIsolationCompatibleCell(t *testing.T) {
	f := newFixture(t)
	_, placement, err := f.service.CreateRelease(context.Background(), f.request("placement-select"))
	if err != nil {
		t.Fatal(err)
	}
	if placement.CellID != f.cell.ID || placement.Region != "eu1" || placement.Isolation != runtimev1.IsolationSandboxed {
		t.Fatalf("placement=%+v", placement)
	}
}
func TestPlacement_RejectsCellWithoutCapacity(t *testing.T) {
	f := newFixture(t)
	err := f.store.Transact(context.Background(), func(tx application.Tx) error {
		cell, _ := tx.GetRuntimeCell(f.cell.ID)
		expected := cell.Version
		cell.CapacityUnits = 1
		cell.AllocatedUnits = 1
		cell.Version++
		return tx.UpdateRuntimeCell(cell, expected)
	})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = f.service.CreateRelease(context.Background(), f.request("placement-full"))
	if !domain.HasCode(err, domain.CodeCapacity) {
		t.Fatalf("err=%v", err)
	}
}
func TestPlacement_IsStickyAcrossDeployments(t *testing.T) {
	f := newFixture(t)
	_, a, err := f.service.CreateRelease(context.Background(), f.request("sticky-a"))
	if err != nil {
		t.Fatal(err)
	}
	req := f.request("sticky-b")
	p := req.Configuration.Processes["web"]
	p.MaxReplicas = 4
	req.Configuration.Processes["web"] = p
	_, b, err := f.service.CreateRelease(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if a.ID != b.ID || a.CellID != b.CellID {
		t.Fatalf("placements=%+v %+v", a, b)
	}
}
func TestPlacement_DrainingCellRejectsNewApplications(t *testing.T) {
	f := newFixture(t)
	err := f.store.Transact(context.Background(), func(tx application.Tx) error {
		cell, _ := tx.GetRuntimeCell(f.cell.ID)
		expected := cell.Version
		if err := cell.Drain(f.clock.Now()); err != nil {
			return err
		}
		return tx.UpdateRuntimeCell(cell, expected)
	})
	if err != nil {
		t.Fatal(err)
	}
	app, env, err := f.service.CreateApplication(context.Background(), application.CreateApplicationRequest{TenantID: "tenant-1", ProjectID: "project-2", Name: "Other", ActorID: "user-1", IdempotencyKey: "other"})
	if err != nil {
		t.Fatal(err)
	}
	req := f.request("draining")
	req.ApplicationID = app.ID
	req.EnvironmentID = env.ID
	_, _, err = f.service.CreateRelease(context.Background(), req)
	if !domain.HasCode(err, domain.CodeCapacity) {
		t.Fatalf("err=%v", err)
	}
}
func TestPlacement_ExplicitMigrationCreatesNewPlacementOperation(t *testing.T) {
	f := newFixture(t)
	_, placement, err := f.service.CreateRelease(context.Background(), f.request("migration-base"))
	if err != nil {
		t.Fatal(err)
	}
	target, err := f.service.RegisterCell(context.Background(), application.RegisterCellRequest{ID: "cell-b", Region: "eu1", Isolation: []runtimev1.IsolationClass{runtimev1.IsolationSandboxed}, CapacityUnits: 100, GitOpsRepository: "https://git.test/runtime-cell-b.git", ClusterServer: "https://cluster-b.test", ArgoProject: "runtime-cell", IngressDomain: "apps-b.eu1.test"})
	if err != nil {
		t.Fatal(err)
	}
	op, err := f.service.RequestPlacementMigration(context.Background(), application.ExplicitMigrationRequest{TenantID: f.app.TenantID, EnvironmentID: f.env.ID, TargetCellID: target.ID, ActorID: "user-1"})
	if err != nil {
		t.Fatal(err)
	}
	if op.FromPlacementID != placement.ID || op.TargetCellID != target.ID || op.State != "PENDING" {
		t.Fatalf("op=%+v", op)
	}
}
