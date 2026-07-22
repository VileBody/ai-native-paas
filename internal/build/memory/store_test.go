package memory_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/keir-research/ai-native-paas/internal/build/application"
	"github.com/keir-research/ai-native-paas/internal/build/domain"
	"github.com/keir-research/ai-native-paas/internal/build/memory"
	sourcev1 "github.com/keir-research/ai-native-paas/pkg/contracts/source/v1"
)

func TestMemoryStore_DoesNotAliasMutableBuildConfig(t *testing.T) {
	store := memory.New()
	now := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
	revision := sourcev1.SourceRevision{ProjectID: "project", RepositoryID: "repository", Branch: "main", CommitSHA: strings.Repeat("a", 40)}
	config := domain.BuildConfig{BuildEnv: map[string]string{"MODE": "release"}, BuildCommand: []string{"go", "build"}, BuildSecretRef: []string{"registry"}}
	identity, err := domain.ComputeBuildIdentity(revision, config, "sha256:"+strings.Repeat("b", 64), "sha256:"+strings.Repeat("c", 64), "v1")
	if err != nil {
		t.Fatal(err)
	}
	build, err := domain.NewBuild("build", "tenant", identity, "correlation", revision, config, "sha256:"+strings.Repeat("b", 64), "sha256:"+strings.Repeat("c", 64), "v1", now)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Transact(context.Background(), func(tx application.Tx) error { return tx.InsertBuild(build) }); err != nil {
		t.Fatal(err)
	}
	build.Config.BuildEnv["MODE"] = "tampered"
	build.Config.BuildCommand[0] = "rm"
	var loaded domain.Build
	if err := store.Transact(context.Background(), func(tx application.Tx) error {
		var ok bool
		loaded, ok = tx.GetBuild("build")
		if !ok {
			t.Fatal("build missing")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	loaded.Config.BuildEnv["MODE"] = "mutated-after-read"
	loaded.Config.BuildCommand[0] = "curl"
	var again domain.Build
	_ = store.Transact(context.Background(), func(tx application.Tx) error { again, _ = tx.GetBuild("build"); return nil })
	if again.Config.BuildEnv["MODE"] != "release" || again.Config.BuildCommand[0] != "go" {
		t.Fatalf("store state aliased: %+v", again.Config)
	}
}

func TestMemoryStore_SnapshotDoesNotAliasArtifactTrustOrEventPayloads(t *testing.T) {
	store := memory.New()
	now := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
	artifact, err := domain.NewArtifact(
		"artifact", "tenant", "build", "registry.test/tenants/tenant/apps/project",
		"sha256:"+strings.Repeat("a", 64), "application/vnd.oci.image.manifest.v1+json", now,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := artifact.Quarantine(now); err != nil {
		t.Fatal(err)
	}
	if err := artifact.AttachSBOM("sha256:"+strings.Repeat("b", 64), "application/spdx+json", now); err != nil {
		t.Fatal(err)
	}
	if err := artifact.ApplyScan(domain.ScanResult{
		Scanner: "scanner", PolicyVersion: "v1", Passed: true,
		FindingsDigest: "sha256:" + strings.Repeat("c", 64), Reasons: []string{"original"}, ScannedAt: now,
	}, now); err != nil {
		t.Fatal(err)
	}
	if err := artifact.AttachSignature(domain.SignatureRecord{
		Issuer: "platform", Algorithm: "ed25519", Digest: artifact.Digest, Signature: "signature",
		AttachmentDigest: "sha256:" + strings.Repeat("d", 64), SignedAt: now,
	}, now); err != nil {
		t.Fatal(err)
	}
	if err := artifact.AttachProvenance("sha256:"+strings.Repeat("e", 64), "application/vnd.dsse.envelope.v1+json", now); err != nil {
		t.Fatal(err)
	}
	if err := artifact.MarkReleasable(now); err != nil {
		t.Fatal(err)
	}
	if err := store.Transact(context.Background(), func(tx application.Tx) error {
		if err := tx.InsertArtifact(artifact); err != nil {
			return err
		}
		if err := tx.AppendOutbox(application.OutboxRecord{ID: "event", Topic: "artifact.releasable.v1", AggregateID: artifact.ID, Payload: []byte(`{"safe":true}`), CreatedAt: now}); err != nil {
			return err
		}
		return tx.AppendAudit(application.AuditRecord{ID: "audit", TenantID: "tenant", ActorID: "actor", Action: "artifact.releasable", ResourceType: "artifact", ResourceID: artifact.ID, Data: []byte(`{"safe":true}`), CreatedAt: now})
	}); err != nil {
		t.Fatal(err)
	}

	_, artifacts, outbox, audit := store.Snapshot()
	artifacts[0].Scan.Reasons[0] = "tampered"
	artifacts[0].Signature.Signature = "tampered"
	outbox[0].Payload[0] = 'X'
	audit[0].Data[0] = 'X'

	_, again, outboxAgain, auditAgain := store.Snapshot()
	if again[0].Scan.Reasons[0] != "original" || again[0].Signature.Signature != "signature" {
		t.Fatalf("artifact snapshot aliased: %+v", again[0])
	}
	if string(outboxAgain[0].Payload) != `{"safe":true}` || string(auditAgain[0].Data) != `{"safe":true}` {
		t.Fatalf("event snapshot aliased: outbox=%s audit=%s", outboxAgain[0].Payload, auditAgain[0].Data)
	}
}

func TestMemoryStore_IdempotencyResultDoesNotAliasCallerBuffer(t *testing.T) {
	store := memory.New()
	result := []byte(`{"build_id":"original"}`)
	record := application.IdempotencyRecord{TenantID: "tenant", Key: "key", Command: "build.request.v1", RequestHash: "hash", Result: result, Completed: true}
	if err := store.Transact(context.Background(), func(tx application.Tx) error { return tx.PutIdempotency(record) }); err != nil {
		t.Fatal(err)
	}
	result[0] = 'X'
	var loaded application.IdempotencyRecord
	if err := store.Transact(context.Background(), func(tx application.Tx) error {
		var ok bool
		loaded, ok = tx.GetIdempotency("tenant", "key")
		if !ok {
			t.Fatal("idempotency record missing")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if string(loaded.Result) != `{"build_id":"original"}` {
		t.Fatalf("stored result aliased caller buffer: %s", loaded.Result)
	}
}
