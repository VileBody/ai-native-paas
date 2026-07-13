//go:build postgres_integration

package integration_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/keir-research/ai-native-paas/internal/runtime/application"
	"github.com/keir-research/ai-native-paas/internal/runtime/domain"
	runtimepostgres "github.com/keir-research/ai-native-paas/internal/runtime/postgres"
	"github.com/keir-research/ai-native-paas/internal/runtime/testkit"
	runtimev1 "github.com/keir-research/ai-native-paas/pkg/contracts/runtime/v1"
)

func migratedRuntimeStore(t *testing.T) (*sql.DB, *runtimepostgres.Store) {
	t.Helper()
	db := openPostgres(t)
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `DROP SCHEMA IF EXISTS runtime CASCADE`); err != nil {
		t.Fatalf("reset runtime schema: %v", err)
	}
	store, err := runtimepostgres.NewStore(db)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("migrate runtime: %v", err)
	}
	return db, store
}

type runtimeFixture struct {
	app        domain.Application
	env        domain.Environment
	cell       domain.RuntimeCell
	placement  domain.Placement
	release    domain.Release
	deployment domain.Deployment
}

func pgRuntimeFixture(t *testing.T, suffix string) runtimeFixture {
	t.Helper()
	now := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
	app, err := domain.NewApplication("app-"+suffix, "tenant-pg", "project-pg", "booking-"+suffix, now)
	if err != nil {
		t.Fatal(err)
	}
	env, err := domain.NewEnvironment("env-"+suffix, app.TenantID, app.ID, "production", true, now)
	if err != nil {
		t.Fatal(err)
	}
	cell, err := domain.NewRuntimeCell("cell-"+suffix, "eu1", []runtimev1.IsolationClass{runtimev1.IsolationSandboxed}, 100, "https://git.example.invalid/runtime-"+suffix+".git", "https://kubernetes.default.svc", "runtime-cell", "apps.eu1.example.invalid", now)
	if err != nil {
		t.Fatal(err)
	}
	placement, err := domain.NewPlacement("placement-"+suffix, app.TenantID, env.ID, cell, runtimev1.IsolationSandboxed, 1, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := cell.Allocate(1, now); err != nil {
		t.Fatal(err)
	}
	if err := env.SetPlacement(placement.ID, now); err != nil {
		t.Fatal(err)
	}
	artifact := testkit.Artifact("a")
	config := testkit.Config()
	release, err := domain.NewRelease("release-"+suffix, app.TenantID, app.ID, env.ID, artifact, config, "policy-v1", now)
	if err != nil {
		t.Fatal(err)
	}
	if err := release.Transition(runtimev1.ReleaseValidated, now); err != nil {
		t.Fatal(err)
	}
	deployment, err := domain.NewDeployment("deployment-"+suffix, release, placement, "", now)
	if err != nil {
		t.Fatal(err)
	}
	return runtimeFixture{app: app, env: env, cell: cell, placement: placement, release: release, deployment: deployment}
}

func insertRuntimeFixture(tx application.Tx, fixture runtimeFixture) error {
	if err := tx.InsertApplication(fixture.app); err != nil {
		return err
	}
	if err := tx.InsertEnvironment(fixture.env); err != nil {
		return err
	}
	if err := tx.InsertRuntimeCell(fixture.cell); err != nil {
		return err
	}
	if err := tx.InsertPlacement(fixture.placement); err != nil {
		return err
	}
	if err := tx.InsertRelease(fixture.release); err != nil {
		return err
	}
	return tx.InsertDeployment(fixture.deployment)
}

func TestPostgres_RuntimeMigrationsCleanInstallAndUpgrade(t *testing.T) {
	db, store := migratedRuntimeStore(t)
	ctx := context.Background()
	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("idempotent migration: %v", err)
	}
	var count int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM runtime.schema_migrations`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 3 {
		t.Fatalf("migration count=%d want=3", count)
	}
	tables := []string{"applications", "environments", "runtime_cells", "placements", "placement_migrations", "releases", "deployments", "gitops_commits", "quarantine", "idempotency", "outbox", "audit"}
	for _, table := range tables {
		var exists bool
		if err := db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_schema='runtime' AND table_name=$1)`, table).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		if !exists {
			t.Fatalf("missing table runtime.%s", table)
		}
	}
}

func TestPostgres_RuntimeApplicationReleaseDeploymentAndOutboxAreAtomic(t *testing.T) {
	db, store := migratedRuntimeStore(t)
	ctx := context.Background()
	fixture := pgRuntimeFixture(t, "atomic")
	outbox := domain.OutboxRecord{ID: "event-atomic", TenantID: fixture.app.TenantID, Topic: "runtime.release.created.v1", AggregateID: fixture.release.ID, Payload: []byte(`{"release_id":"release-atomic"}`), CreatedAt: fixture.release.CreatedAt}
	sentinel := errors.New("force rollback")

	err := store.Transact(ctx, func(tx application.Tx) error {
		if err := insertRuntimeFixture(tx, fixture); err != nil {
			return err
		}
		if err := tx.AppendOutbox(outbox); err != nil {
			return err
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("rollback error=%v", err)
	}
	for _, table := range []string{"applications", "environments", "runtime_cells", "placements", "releases", "deployments", "outbox"} {
		var count int
		if err := db.QueryRowContext(ctx, `SELECT count(*) FROM runtime.`+table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("runtime.%s count after rollback=%d", table, count)
		}
	}

	if err := store.Transact(ctx, func(tx application.Tx) error {
		if err := insertRuntimeFixture(tx, fixture); err != nil {
			return err
		}
		return tx.AppendOutbox(outbox)
	}); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"applications", "environments", "runtime_cells", "placements", "releases", "deployments", "outbox"} {
		var count int
		if err := db.QueryRowContext(ctx, `SELECT count(*) FROM runtime.`+table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("runtime.%s count after commit=%d want=1", table, count)
		}
	}
}

func TestPostgres_RuntimeConcurrentReleaseIdentityUsesSingleWinner(t *testing.T) {
	db, store := migratedRuntimeStore(t)
	clock := &testkit.Clock{T: time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)}
	ids := &testkit.IDs{}
	service := application.Service{
		Store:     store,
		Artifacts: &testkit.ArtifactPolicy{Allowed: true},
		Units:     application.StaticUnitCatalog{"u1": 1},
		Clock:     clock,
		IDs:       ids,
		Scheduler: application.DeterministicScheduler{},
	}
	ctx := context.Background()
	if _, err := service.RegisterCell(ctx, application.RegisterCellRequest{ID: "cell-a", Region: "eu1", Isolation: []runtimev1.IsolationClass{runtimev1.IsolationSandboxed}, CapacityUnits: 100, GitOpsRepository: "https://git.example.invalid/runtime.git", ClusterServer: "https://kubernetes.default.svc", ArgoProject: "runtime-cell", IngressDomain: "apps.eu1.example.invalid"}); err != nil {
		t.Fatal(err)
	}
	app, env, err := service.CreateApplication(ctx, application.CreateApplicationRequest{TenantID: "tenant-pg", ProjectID: "project-pg", Name: "booking", ActorID: "user-pg", IdempotencyKey: "create-app"})
	if err != nil {
		t.Fatal(err)
	}

	const workers = 16
	var group sync.WaitGroup
	idsCh := make(chan string, workers)
	errorsCh := make(chan error, workers)
	group.Add(workers)
	for i := 0; i < workers; i++ {
		i := i
		go func() {
			defer group.Done()
			release, _, err := service.CreateRelease(context.Background(), runtimev1.DeployRequest{
				TenantID: app.TenantID, ApplicationID: app.ID, EnvironmentID: env.ID,
				Artifact: testkit.Artifact("a"), Configuration: testkit.Config(),
				IdempotencyKey: fmt.Sprintf("release-%d", i), ActorID: "user-pg",
			})
			if err != nil {
				errorsCh <- err
				return
			}
			idsCh <- release.ID
		}()
	}
	group.Wait()
	close(idsCh)
	close(errorsCh)
	for err := range errorsCh {
		t.Errorf("concurrent release: %v", err)
	}
	winners := map[string]struct{}{}
	for id := range idsCh {
		winners[id] = struct{}{}
	}
	if len(winners) != 1 {
		t.Fatalf("release winners=%v want exactly one", winners)
	}
	var releaseCount, placementCount int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM runtime.releases`).Scan(&releaseCount); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM runtime.placements WHERE current`).Scan(&placementCount); err != nil {
		t.Fatal(err)
	}
	if releaseCount != 1 || placementCount != 1 {
		t.Fatalf("releaseCount=%d placementCount=%d", releaseCount, placementCount)
	}
}

func TestPostgres_RuntimeOptimisticLockPreventsLostUpdate(t *testing.T) {
	_, store := migratedRuntimeStore(t)
	ctx := context.Background()
	fixture := pgRuntimeFixture(t, "lock")
	if err := store.Transact(ctx, func(tx application.Tx) error { return tx.InsertApplication(fixture.app) }); err != nil {
		t.Fatal(err)
	}

	first := fixture.app
	expected := first.Version
	first.Name = "booking-one"
	first.Version++
	first.UpdatedAt = first.UpdatedAt.Add(time.Second)
	if err := store.Transact(ctx, func(tx application.Tx) error { return tx.UpdateApplication(first, expected) }); err != nil {
		t.Fatal(err)
	}

	stale := fixture.app
	stale.Name = "booking-two"
	stale.Version++
	stale.UpdatedAt = stale.UpdatedAt.Add(2 * time.Second)
	err := store.Transact(ctx, func(tx application.Tx) error { return tx.UpdateApplication(stale, expected) })
	if !domain.HasCode(err, domain.CodeStaleVersion) {
		t.Fatalf("stale update error=%v", err)
	}
}

func TestPostgres_RuntimeReleaseArtifactAndConfigAreImmutable(t *testing.T) {
	db, store := migratedRuntimeStore(t)
	ctx := context.Background()
	fixture := pgRuntimeFixture(t, "immutable")
	if err := store.Transact(ctx, func(tx application.Tx) error { return insertRuntimeFixture(tx, fixture) }); err != nil {
		t.Fatal(err)
	}
	replacement := "sha256:" + strings.Repeat("b", 64)
	_, err := db.ExecContext(ctx, `UPDATE runtime.releases SET digest=$1, payload=jsonb_set(payload, '{Artifact,digest}', to_jsonb($1::text), false) WHERE id=$2`, replacement, fixture.release.ID)
	if err == nil {
		t.Fatal("release digest mutation unexpectedly succeeded")
	}
	_, err = db.ExecContext(ctx, `UPDATE runtime.releases SET payload=jsonb_set(payload, '{Configuration,unit}', '"u2"'::jsonb, false) WHERE id=$1`, fixture.release.ID)
	if err == nil {
		t.Fatal("release configuration mutation unexpectedly succeeded")
	}
}

func TestPostgres_RuntimeGitOpsCommitIsImmutable(t *testing.T) {
	db, store := migratedRuntimeStore(t)
	ctx := context.Background()
	fixture := pgRuntimeFixture(t, "gitops")
	if err := store.Transact(ctx, func(tx application.Tx) error { return insertRuntimeFixture(tx, fixture) }); err != nil {
		t.Fatal(err)
	}
	record := domain.GitOpsCommitRecord{ID: "commit-record", TenantID: fixture.app.TenantID, CellID: fixture.cell.ID, ReleaseID: fixture.release.ID, DeploymentID: fixture.deployment.ID, Path: "cells/cell-gitops/tenants/tenant-pg/apps/app-gitops/production", ManifestHash: "sha256:" + strings.Repeat("c", 64), CommitSHA: strings.Repeat("d", 40), CreatedAt: fixture.release.CreatedAt}
	if err := store.Transact(ctx, func(tx application.Tx) error { return tx.InsertGitOpsCommit(record) }); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE runtime.gitops_commits SET commit_sha=$1 WHERE id=$2`, strings.Repeat("e", 40), record.ID); err == nil {
		t.Fatal("GitOps commit UPDATE unexpectedly succeeded")
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM runtime.gitops_commits WHERE id=$1`, record.ID); err == nil {
		t.Fatal("GitOps commit DELETE unexpectedly succeeded")
	}
}

func TestPostgres_RuntimeAuditRejectsMutation(t *testing.T) {
	db, store := migratedRuntimeStore(t)
	ctx := context.Background()
	record := domain.AuditRecord{ID: "audit-runtime", TenantID: "tenant-pg", ActorID: "actor-pg", Action: "runtime.release.create", ResourceType: "release", ResourceID: "release-pg", Data: []byte(`{"safe":true}`), CreatedAt: time.Now().UTC()}
	if err := store.Transact(ctx, func(tx application.Tx) error { return tx.AppendAudit(record) }); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE runtime.audit SET action='tampered' WHERE id=$1`, record.ID); err == nil {
		t.Fatal("runtime audit UPDATE unexpectedly succeeded")
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM runtime.audit WHERE id=$1`, record.ID); err == nil {
		t.Fatal("runtime audit DELETE unexpectedly succeeded")
	}
}

func TestPostgres_RuntimePayloadConsistencyRejectsColumnDrift(t *testing.T) {
	db, store := migratedRuntimeStore(t)
	ctx := context.Background()
	fixture := pgRuntimeFixture(t, "payload")
	if err := store.Transact(ctx, func(tx application.Tx) error { return insertRuntimeFixture(tx, fixture) }); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE runtime.applications SET name='column-only' WHERE id=$1`, fixture.app.ID); err == nil {
		t.Fatal("application column/payload drift unexpectedly succeeded")
	}
	if _, err := db.ExecContext(ctx, `UPDATE runtime.deployments SET phase='READY' WHERE id=$1`, fixture.deployment.ID); err == nil {
		t.Fatal("deployment column/payload drift unexpectedly succeeded")
	}
}

func TestPostgres_RuntimeOnlyOneActiveReleasePerEnvironment(t *testing.T) {
	_, store := migratedRuntimeStore(t)
	ctx := context.Background()
	fixture := pgRuntimeFixture(t, "active")
	second := fixture.release
	second.ID = "release-active-two"
	second.Artifact = testkit.Artifact("b")
	second.Configuration = testkit.Config()
	second.Configuration.GeneratedHostname = "other.apps.eu1.example.invalid"
	created, err := domain.NewRelease(second.ID, second.TenantID, second.ApplicationID, second.EnvironmentID, second.Artifact, second.Configuration, "policy-v1", second.CreatedAt)
	if err != nil {
		t.Fatal(err)
	}
	second = created
	if err := second.Transition(runtimev1.ReleaseValidated, second.UpdatedAt); err != nil {
		t.Fatal(err)
	}
	if err := store.Transact(ctx, func(tx application.Tx) error {
		if err := insertRuntimeFixture(tx, fixture); err != nil {
			return err
		}
		return tx.InsertRelease(second)
	}); err != nil {
		t.Fatal(err)
	}
	activate := func(id string) error {
		return store.Transact(ctx, func(tx application.Tx) error {
			value, ok := tx.GetRelease(id)
			if !ok {
				return errors.New("release missing")
			}
			expected := value.Version
			for _, state := range []runtimev1.ReleaseState{runtimev1.ReleaseCommittedToGitOps, runtimev1.ReleaseDeploying, runtimev1.ReleaseActive} {
				if err := value.Transition(state, value.UpdatedAt.Add(time.Second)); err != nil {
					return err
				}
			}
			return tx.UpdateRelease(value, expected)
		})
	}
	if err := activate(fixture.release.ID); err != nil {
		t.Fatal(err)
	}
	if err := activate(second.ID); err == nil {
		t.Fatal("second active release unexpectedly succeeded")
	}
}
