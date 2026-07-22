//go:build postgres_integration

package integration_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/keir-research/ai-native-paas/internal/build/application"
	"github.com/keir-research/ai-native-paas/internal/build/domain"
	"github.com/keir-research/ai-native-paas/internal/build/logs"
	buildpostgres "github.com/keir-research/ai-native-paas/internal/build/postgres"
	"github.com/keir-research/ai-native-paas/internal/build/support"
	"github.com/keir-research/ai-native-paas/internal/build/testkit"
	buildv1 "github.com/keir-research/ai-native-paas/pkg/contracts/build/v1"
	buildv2 "github.com/keir-research/ai-native-paas/pkg/contracts/build/v2"
	sourcev1 "github.com/keir-research/ai-native-paas/pkg/contracts/source/v1"
)

func migratedBuildStore(t *testing.T) (*sql.DB, *buildpostgres.Store) {
	t.Helper()
	db := openPostgres(t)
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `DROP SCHEMA IF EXISTS build CASCADE`); err != nil {
		t.Fatalf("reset build schema: %v", err)
	}
	store, err := buildpostgres.NewStore(db)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("migrate build: %v", err)
	}
	return db, store
}

func pgBuild(t *testing.T, id string) domain.Build {
	t.Helper()
	now := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
	revision := sourcev1.SourceRevision{ProjectID: "project-pg", RepositoryID: "repository-pg", Branch: "main", CommitSHA: strings.Repeat("a", 40)}
	identity, err := domain.ComputeBuildIdentity(revision, domain.BuildConfig{}, "sha256:"+strings.Repeat("b", 64), "sha256:"+strings.Repeat("c", 64), "v1")
	if err != nil {
		t.Fatal(err)
	}
	value, err := domain.NewBuild(id, "tenant-pg", identity, "correlation-pg", revision, domain.BuildConfig{}, "sha256:"+strings.Repeat("b", 64), "sha256:"+strings.Repeat("c", 64), "v1", now)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func TestPostgres_BuildMigrationsCleanInstallAndUpgrade(t *testing.T) {
	db, store := migratedBuildStore(t)
	ctx := context.Background()
	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("idempotent migration: %v", err)
	}
	var count int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM build.schema_migrations`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 7 {
		t.Fatalf("migration count=%d want=7", count)
	}
	for _, table := range []string{"builds", "artifacts", "scan_results", "signature_records", "log_refs", "idempotency", "outbox", "audit"} {
		var exists bool
		if err := db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_schema='build' AND table_name=$1)`, table).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		if !exists {
			t.Fatalf("missing table build.%s", table)
		}
	}
}

func TestPostgres_BuildAndOutboxAreAtomic(t *testing.T) {
	db, store := migratedBuildStore(t)
	ctx := context.Background()
	value := pgBuild(t, "build-atomic")
	sentinel := errors.New("rollback")
	err := store.Transact(ctx, func(tx application.Tx) error {
		if err := tx.InsertBuild(value); err != nil {
			return err
		}
		if err := tx.AppendOutbox(application.OutboxRecord{ID: "event-atomic", Topic: "build.requested.v1", AggregateID: value.ID, Payload: []byte(`{"build_id":"build-atomic"}`), CreatedAt: value.CreatedAt}); err != nil {
			return err
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("rollback error=%v", err)
	}
	for _, table := range []string{"builds", "outbox"} {
		var count int
		if err := db.QueryRowContext(ctx, `SELECT count(*) FROM build.`+table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("%s count after rollback=%d", table, count)
		}
	}
}

func TestPostgres_ConcurrentBuildIdentityUsesSingleWinner(t *testing.T) {
	_, store := migratedBuildStore(t)
	service := &application.Service{Store: store, Clock: support.Clock{}, IDs: &support.IDs{}}
	command := application.RequestBuildCommand{
		TenantID: "tenant-pg", ActorID: "actor-pg", CorrelationID: "correlation-pg", IdempotencyKey: "same-key",
		Source:        sourcev1.SourceRevision{ProjectID: "project-pg", RepositoryID: "repository-pg", Branch: "main", CommitSHA: strings.Repeat("d", 40)},
		BuilderDigest: "sha256:" + strings.Repeat("e", 64), RunImageDigest: "sha256:" + strings.Repeat("f", 64), PlatformVersion: "v1",
	}
	const workers = 24
	var group sync.WaitGroup
	ids := make(chan string, workers)
	errorsCh := make(chan error, workers)
	group.Add(workers)
	for range workers {
		go func() {
			defer group.Done()
			result, err := service.RequestBuild(context.Background(), command)
			if err != nil {
				errorsCh <- err
				return
			}
			ids <- result.Build.ID
		}()
	}
	group.Wait()
	close(ids)
	close(errorsCh)
	for err := range errorsCh {
		t.Errorf("request: %v", err)
	}
	seen := map[string]struct{}{}
	for id := range ids {
		seen[id] = struct{}{}
	}
	if len(seen) != 1 {
		t.Fatalf("build ids=%v", seen)
	}
}

func TestPostgres_BuildOptimisticLockPreventsLostUpdate(t *testing.T) {
	_, store := migratedBuildStore(t)
	ctx := context.Background()
	value := pgBuild(t, "build-lock")
	if err := store.Transact(ctx, func(tx application.Tx) error { return tx.InsertBuild(value) }); err != nil {
		t.Fatal(err)
	}
	var first, second domain.Build
	if err := store.Transact(ctx, func(tx application.Tx) error {
		var ok bool
		first, ok = tx.GetBuild(value.ID)
		if !ok {
			return errors.New("first read missing")
		}
		second, ok = tx.GetBuild(value.ID)
		if !ok {
			return errors.New("second read missing")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	now := value.CreatedAt.Add(time.Second)
	if err := first.Transition(buildv1.BuildFetchingSource, now); err != nil {
		t.Fatal(err)
	}
	if err := second.Cancel(now); err != nil {
		t.Fatal(err)
	}
	if err := store.Transact(ctx, func(tx application.Tx) error { return tx.UpdateBuild(first, 1) }); err != nil {
		t.Fatal(err)
	}
	err := store.Transact(ctx, func(tx application.Tx) error { return tx.UpdateBuild(second, 1) })
	if !domain.HasCode(err, domain.CodeStaleVersion) {
		t.Fatalf("second update=%v", err)
	}
}

func TestPostgres_ArtifactIdentityIsImmutable(t *testing.T) {
	db, store := migratedBuildStore(t)
	ctx := context.Background()
	value := pgBuild(t, "build-artifact")
	artifact, err := domain.NewArtifact("artifact-pg", value.TenantID, value.ID, "registry.test/tenants/tenant-pg/apps/project-pg", "sha256:"+strings.Repeat("1", 64), "application/vnd.oci.image.manifest.v1+json", value.CreatedAt)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Transact(ctx, func(tx application.Tx) error {
		if err := tx.InsertBuild(value); err != nil {
			return err
		}
		return tx.InsertArtifact(artifact)
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE build.artifacts SET digest=$1 WHERE id=$2`, "sha256:"+strings.Repeat("2", 64), artifact.ID); err == nil {
		t.Fatal("artifact digest mutation unexpectedly succeeded")
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO build.signature_records(artifact_id,issuer,algorithm,digest,signature,signed_at) VALUES($1,'platform','ed25519',$2,'sig',now())`, artifact.ID, "sha256:"+strings.Repeat("3", 64)); err == nil {
		t.Fatal("mismatched signature digest unexpectedly succeeded")
	}
}

func TestPostgres_BuildAuditRejectsMutation(t *testing.T) {
	db, store := migratedBuildStore(t)
	ctx := context.Background()
	record := application.AuditRecord{ID: "audit-pg", TenantID: "tenant-pg", ActorID: "actor-pg", Action: "build.request", ResourceType: "build", ResourceID: "build-pg", Data: []byte(`{"safe":true}`), CreatedAt: time.Now().UTC()}
	if err := store.Transact(ctx, func(tx application.Tx) error { return tx.AppendAudit(record) }); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE build.audit SET action='tampered' WHERE id='audit-pg'`); err == nil {
		t.Fatal("audit UPDATE unexpectedly succeeded")
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM build.audit WHERE id='audit-pg'`); err == nil {
		t.Fatal("audit DELETE unexpectedly succeeded")
	}
}

func pgRawDigest(raw []byte) string {
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func TestPostgres_BuildPipelinePersistsReleasableTrustChain(t *testing.T) {
	db, store := migratedBuildStore(t)
	clock := &testkit.Clock{T: time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)}
	sourcePath := t.TempDir()
	if err := os.WriteFile(sourcePath+"/main.go", []byte("package main"), 0600); err != nil {
		t.Fatal(err)
	}
	manifestDigest := "sha256:" + strings.Repeat("a", 64)
	provenanceAttestor, provenanceVerifier := testkit.ProvenanceFakes([]byte("provenance"))
	service := &application.Service{
		Store:      store,
		Fetcher:    &testkit.Fetcher{Snapshot: application.SourceSnapshot{Path: sourcePath}},
		Detector:   &testkit.Detector{Detection: application.Detection{Runtime: "go", Backend: application.BackendBuildpacks, BuildpackID: "paketo/go"}},
		Buildpacks: &testkit.Builder{Output: application.BuildOutput{ManifestDigest: manifestDigest, MediaType: "application/vnd.oci.image.manifest.v1+json"}},
		Registry: &testkit.Registry{Published: application.PublishedArtifact{
			Repository: "registry.test/tenants/tenant-pg/apps/project-pg", Digest: manifestDigest, MediaType: "application/vnd.oci.image.manifest.v1+json",
		}},
		SBOM:       testkit.SBOM{Result: application.SBOMResult{Digest: pgRawDigest([]byte("sbom")), MediaType: "application/spdx+json", Document: []byte("sbom")}},
		Scanner:    testkit.Scanner{Result: domain.ScanResult{Scanner: "test", PolicyVersion: "v1", Passed: true, FindingsDigest: "sha256:" + strings.Repeat("c", 64), ScannedAt: clock.Now()}},
		Signer:     testkit.Signer{Record: domain.SignatureRecord{Issuer: "platform", Algorithm: "ed25519", Digest: manifestDigest, Signature: "signature", SignedAt: clock.Now()}},
		Verifier:   &testkit.Verifier{},
		Provenance: provenanceAttestor, ProvenanceVerifier: provenanceVerifier,
		Logs: logs.New(), Clock: clock, IDs: &testkit.IDs{}, RepositoryBase: "registry.test/tenants",
	}
	requested, err := service.RequestBuild(context.Background(), application.RequestBuildCommand{
		TenantID: "tenant-pg", ActorID: "actor-pg", CorrelationID: "correlation-pipeline", IdempotencyKey: "pipeline",
		Source:        sourcev1.SourceRevision{ProjectID: "project-pg", RepositoryID: "repository-pg", Branch: "main", CommitSHA: strings.Repeat("d", 40)},
		BuilderDigest: "sha256:" + strings.Repeat("e", 64), RunImageDigest: "sha256:" + strings.Repeat("f", 64), PlatformVersion: "v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	completed, artifact, err := service.RunBuild(context.Background(), "tenant-pg", "actor-pg", requested.Build.ID)
	if err != nil {
		t.Fatal(err)
	}
	if completed.State != buildv1.BuildSucceeded || completed.Backend != domain.ExecutionBuildpacks || completed.Runtime != "go" || completed.BuildpackID != "paketo/go" {
		t.Fatalf("completed=%+v", completed)
	}
	if artifact == nil || artifact.State != domain.ArtifactReleasable || artifact.Signature == nil || !buildv1.ValidDigest(artifact.Signature.AttachmentDigest) {
		t.Fatalf("artifact=%+v", artifact)
	}
	var storedState, storedBackend, storedRuntime, storedBuildpack string
	if err := db.QueryRowContext(context.Background(), `SELECT state,backend,runtime,buildpack_id FROM build.builds WHERE id=$1`, completed.ID).Scan(&storedState, &storedBackend, &storedRuntime, &storedBuildpack); err != nil {
		t.Fatal(err)
	}
	if storedState != "SUCCEEDED" || storedBackend != "buildpacks" || storedRuntime != "go" || storedBuildpack != "paketo/go" {
		t.Fatalf("stored state=%s backend=%s runtime=%s buildpack=%s", storedState, storedBackend, storedRuntime, storedBuildpack)
	}
	if _, err := db.ExecContext(context.Background(), `UPDATE build.signature_records SET signature='tampered' WHERE artifact_id=$1`, artifact.ID); err == nil {
		t.Fatal("signature mutation unexpectedly succeeded")
	}
	if _, err := db.ExecContext(context.Background(), `UPDATE build.scan_results SET passed=false WHERE artifact_id=$1`, artifact.ID); err == nil {
		t.Fatal("scan mutation unexpectedly succeeded")
	}
	if _, err := db.ExecContext(context.Background(), `UPDATE build.artifacts SET state='QUARANTINED',version=version+1 WHERE id=$1`, artifact.ID); err == nil {
		t.Fatal("artifact state regression unexpectedly succeeded")
	}
	if _, err := db.ExecContext(context.Background(), `UPDATE build.artifacts SET sbom_digest=$2,version=version+1 WHERE id=$1`, artifact.ID, "sha256:"+strings.Repeat("9", 64)); err == nil {
		t.Fatal("artifact SBOM mutation unexpectedly succeeded")
	}
	if _, err := db.ExecContext(context.Background(), `UPDATE build.artifacts SET id='renamed-artifact',version=version+1 WHERE id=$1`, artifact.ID); err == nil {
		t.Fatal("artifact primary identity mutation unexpectedly succeeded")
	}
	if _, err := db.ExecContext(context.Background(), `UPDATE build.builds SET identity='tampered',version=version+1 WHERE id=$1`, completed.ID); err == nil {
		t.Fatal("build identity mutation unexpectedly succeeded")
	}
}

func TestPostgres_DockerfileExecutionSelectionAndIdentityRemainImmutable(t *testing.T) {
	db, store := migratedBuildStore(t)
	ctx := context.Background()
	build := pgBuild(t, "build-dockerfile")
	if err := store.Transact(ctx, func(tx application.Tx) error { return tx.InsertBuild(build) }); err != nil {
		t.Fatal(err)
	}
	persist := func(mutate func(*domain.Build) error) {
		expected := build.Version
		if err := mutate(&build); err != nil {
			t.Fatal(err)
		}
		if err := store.Transact(ctx, func(tx application.Tx) error { return tx.UpdateBuild(build, expected) }); err != nil {
			t.Fatal(err)
		}
	}
	persist(func(value *domain.Build) error {
		return value.Transition(buildv1.BuildFetchingSource, time.Now().UTC())
	})
	persist(func(value *domain.Build) error { return value.Transition(buildv1.BuildDetecting, time.Now().UTC()) })
	persist(func(value *domain.Build) error {
		return value.SelectExecution("dockerfile", domain.ExecutionDockerfile, "", time.Now().UTC())
	})

	if _, err := db.ExecContext(ctx, `UPDATE build.builds SET buildpack_id='malicious/buildpack',version=version+1 WHERE id=$1`, build.ID); err == nil {
		t.Fatal("dockerfile buildpack mutation unexpectedly succeeded")
	}
	if _, err := db.ExecContext(ctx, `UPDATE build.builds SET correlation_id='tampered',version=version+1 WHERE id=$1`, build.ID); err == nil {
		t.Fatal("build correlation identity mutation unexpectedly succeeded")
	}
	if _, err := db.ExecContext(ctx, `UPDATE build.builds SET id='renamed-build',version=version+1 WHERE id=$1`, build.ID); err == nil {
		t.Fatal("build primary identity mutation unexpectedly succeeded")
	}
}

func TestPostgres_V2BuildSpecRoundTripsAndRemainsImmutable(t *testing.T) {
	db, store := migratedBuildStore(t)
	ctx := context.Background()
	build := pgBuild(t, "build-v2-spec")
	spec := buildv2.BuildSpec{
		SourceSHA: build.Source.CommitSHA, Driver: buildv2.DriverDockerfile,
		DefinitionPath: "docker/Dockerfile", Platforms: []string{"linux/amd64"},
		NetworkProfile: "governed", CacheScope: "project-pg", ResourceClass: "standard", TimeoutSeconds: 900,
	}
	if err := build.ConfigureV2Request(&spec, false); err != nil {
		t.Fatal(err)
	}
	if err := store.Transact(ctx, func(tx application.Tx) error { return tx.InsertBuild(build) }); err != nil {
		t.Fatal(err)
	}
	var loaded domain.Build
	if err := store.Transact(ctx, func(tx application.Tx) error {
		var ok bool
		loaded, ok = tx.GetBuild(build.ID)
		if !ok {
			return errors.New("v2 build missing")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if loaded.RequestContract != buildv2.APIVersion || loaded.BuildSpec == nil || loaded.BuildSpec.DefinitionPath != "docker/Dockerfile" || loaded.BuildSpecDigest == "" || loaded.AutoDetectionAllowed {
		t.Fatalf("loaded=%+v", loaded)
	}
	if _, err := db.ExecContext(ctx, `UPDATE build.builds SET config=jsonb_set(config,'{_build_spec,definition_path}','"SharedDockerSocket"'::jsonb),version=version+1 WHERE id=$1`, build.ID); err == nil {
		t.Fatal("persisted v2 build spec mutation unexpectedly succeeded")
	}
}

func TestBuild_TrustChainRequiredBeforeArtifactReleasable(t *testing.T) {
	db, store := migratedBuildStore(t)
	ctx := context.Background()
	build := pgBuild(t, "build-trust-gate")
	artifact, err := domain.NewArtifact("artifact-trust-gate", build.TenantID, build.ID, "registry.test/tenants/tenant-pg/apps/project-pg", "sha256:"+strings.Repeat("1", 64), "application/vnd.oci.image.manifest.v1+json", build.CreatedAt)
	if err != nil {
		t.Fatal(err)
	}
	if err := artifact.Quarantine(build.CreatedAt); err != nil {
		t.Fatal(err)
	}
	if err := store.Transact(ctx, func(tx application.Tx) error {
		if err := tx.InsertBuild(build); err != nil {
			return err
		}
		return tx.InsertArtifact(artifact)
	}); err != nil {
		t.Fatal(err)
	}
	service := &application.Service{Store: store}
	assertDenied := func(stage string) {
		t.Helper()
		decision, err := service.EvaluateArtifact(ctx, build.TenantID, artifact.ID)
		if err != nil || decision.Allowed {
			t.Fatalf("%s decision=%+v err=%v", stage, decision, err)
		}
	}
	assertDenied("digest only")
	if _, err := db.ExecContext(ctx, `UPDATE build.artifacts SET state='RELEASABLE',version=version+1 WHERE id=$1`, artifact.ID); err == nil {
		t.Fatal("database allowed releasable artifact without trust records")
	}
	persist := func(mutate func(*domain.Artifact) error) {
		t.Helper()
		expected := artifact.Version
		if err := mutate(&artifact); err != nil {
			t.Fatal(err)
		}
		if err := store.Transact(ctx, func(tx application.Tx) error { return tx.UpdateArtifact(artifact, expected) }); err != nil {
			t.Fatal(err)
		}
	}
	persist(func(value *domain.Artifact) error {
		return value.AttachSBOM("sha256:"+strings.Repeat("2", 64), "application/spdx+json", build.CreatedAt.Add(time.Second))
	})
	assertDenied("SBOM only")
	persist(func(value *domain.Artifact) error {
		return value.ApplyScan(domain.ScanResult{Scanner: "scanner", PolicyVersion: "policy-v1", Passed: true, FindingsDigest: "sha256:" + strings.Repeat("3", 64), ScannedAt: build.CreatedAt.Add(2 * time.Second)}, build.CreatedAt.Add(2*time.Second))
	})
	assertDenied("SBOM and scan")
	persist(func(value *domain.Artifact) error {
		return value.AttachSignature(domain.SignatureRecord{Issuer: "platform", Algorithm: "ed25519", Digest: value.Digest, Signature: "signature", AttachmentDigest: "sha256:" + strings.Repeat("4", 64), SignedAt: build.CreatedAt.Add(3 * time.Second)}, build.CreatedAt.Add(3*time.Second))
	})
	assertDenied("signature without provenance")
	persist(func(value *domain.Artifact) error {
		return value.AttachProvenance("sha256:"+strings.Repeat("5", 64), "application/vnd.dsse.envelope.v1+json", build.CreatedAt.Add(4*time.Second))
	})
	assertDenied("complete records before release transition")
	persist(func(value *domain.Artifact) error { return value.MarkReleasable(build.CreatedAt.Add(5 * time.Second)) })
	decision, err := service.EvaluateArtifact(ctx, build.TenantID, artifact.ID)
	if err != nil || !decision.Allowed {
		t.Fatalf("complete trust chain decision=%+v err=%v", decision, err)
	}
}

func TestBuild_SameIdentityConcurrentRequestsExecuteOnce(t *testing.T) {
	db, store := migratedBuildStore(t)
	clock := &testkit.Clock{T: time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)}
	builder := &testkit.Builder{Output: application.BuildOutput{ManifestDigest: "sha256:" + strings.Repeat("a", 64), MediaType: "application/vnd.oci.image.manifest.v1+json"}}
	provenanceAttestor, provenanceVerifier := testkit.ProvenanceFakes([]byte("provenance"))
	service := &application.Service{
		Store: store, Fetcher: &testkit.Fetcher{Snapshot: application.SourceSnapshot{Path: t.TempDir()}},
		Detector:   &testkit.Detector{Detection: application.Detection{Runtime: "go", Backend: application.BackendBuildpacks, BuildpackID: "paketo/go"}},
		Buildpacks: builder,
		Registry:   &testkit.Registry{Published: application.PublishedArtifact{Repository: "registry.test/tenants/tenant-pg/apps/project-pg", Digest: "sha256:" + strings.Repeat("a", 64), MediaType: "application/vnd.oci.image.manifest.v1+json"}},
		SBOM:       testkit.SBOM{Result: application.SBOMResult{Digest: pgRawDigest([]byte("sbom")), MediaType: "application/spdx+json", Document: []byte("sbom")}},
		Scanner:    testkit.Scanner{Result: domain.ScanResult{Scanner: "scanner", PolicyVersion: "v1", Passed: true, FindingsDigest: "sha256:" + strings.Repeat("c", 64), ScannedAt: clock.Now()}},
		Signer:     testkit.Signer{Record: domain.SignatureRecord{Issuer: "platform", Algorithm: "ed25519", Digest: "sha256:" + strings.Repeat("a", 64), Signature: "signature", SignedAt: clock.Now()}},
		Verifier:   &testkit.Verifier{}, Provenance: provenanceAttestor, ProvenanceVerifier: provenanceVerifier,
		Logs: logs.New(), Clock: clock, IDs: &testkit.IDs{}, RepositoryBase: "registry.test/tenants",
	}
	base := application.RequestBuildCommand{
		TenantID: "tenant-pg", ActorID: "actor-pg", CorrelationID: "correlation-concurrent",
		Source:        sourcev1.SourceRevision{ProjectID: "project-pg", RepositoryID: "repository-pg", Branch: "main", CommitSHA: strings.Repeat("d", 40)},
		BuilderDigest: "sha256:" + strings.Repeat("e", 64), RunImageDigest: "sha256:" + strings.Repeat("f", 64), PlatformVersion: "v2",
	}
	const workers = 20
	buildIDs := make(chan string, workers)
	errorsChannel := make(chan error, workers)
	var group sync.WaitGroup
	for index := range workers {
		group.Add(1)
		go func() {
			defer group.Done()
			command := base
			command.IdempotencyKey = fmt.Sprintf("request-%d", index)
			result, err := service.RequestBuild(context.Background(), command)
			if err != nil {
				errorsChannel <- err
				return
			}
			buildIDs <- result.Build.ID
		}()
	}
	group.Wait()
	close(buildIDs)
	close(errorsChannel)
	for err := range errorsChannel {
		t.Errorf("request: %v", err)
	}
	uniqueIDs := map[string]struct{}{}
	for id := range buildIDs {
		uniqueIDs[id] = struct{}{}
	}
	if len(uniqueIDs) != 1 {
		t.Fatalf("build ids=%v", uniqueIDs)
	}
	var buildID string
	for id := range uniqueIDs {
		buildID = id
	}

	runErrors := make(chan error, workers)
	observedIDs := make(chan string, workers)
	for range workers {
		group.Add(1)
		go func() {
			defer group.Done()
			build, _, err := service.RunBuild(context.Background(), "tenant-pg", "actor-pg", buildID)
			if err != nil {
				runErrors <- err
				return
			}
			observedIDs <- build.ID
		}()
	}
	group.Wait()
	close(runErrors)
	close(observedIDs)
	for err := range runErrors {
		t.Errorf("run: %v", err)
	}
	observedCount := 0
	for id := range observedIDs {
		observedCount++
		if id != buildID {
			t.Errorf("observed build id=%s want=%s", id, buildID)
		}
	}
	if observedCount != workers {
		t.Fatalf("observers=%d want=%d", observedCount, workers)
	}
	if len(builder.Requests) != 1 {
		t.Fatalf("builder executions=%d", len(builder.Requests))
	}
	var artifacts, startedEvents int
	if err := db.QueryRow(`SELECT count(*) FROM build.artifacts WHERE build_id=$1`, buildID).Scan(&artifacts); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM build.outbox WHERE aggregate_id=$1 AND topic='build.started.v1'`, buildID).Scan(&startedEvents); err != nil {
		t.Fatal(err)
	}
	if artifacts != 1 || startedEvents != 1 {
		t.Fatalf("artifacts=%d started events=%d", artifacts, startedEvents)
	}
}
