//go:build postgres_integration

package integration_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/keir-research/ai-native-paas/internal/workspace"
	workspacepostgres "github.com/keir-research/ai-native-paas/internal/workspace/postgres"
	workspacev1 "github.com/keir-research/ai-native-paas/pkg/contracts/workspace/v1"
)

func migratedWorkspaceStore(t *testing.T) (*sql.DB, *workspacepostgres.Store) {
	t.Helper()
	db := openPostgres(t)
	if _, err := db.Exec(`DROP SCHEMA IF EXISTS workspace CASCADE`); err != nil {
		t.Fatal(err)
	}
	store := &workspacepostgres.Store{DB: db}
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatalf("idempotent workspace migration: %v", err)
	}
	return db, store
}

func postgresWorkspace(now time.Time) workspace.Workspace {
	spec := workspacev1.WorkspaceSpec{
		ProjectID: "project-pg", TaskID: "task-pg", ImageDigest: "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		CPUMillis: 2000, MemoryMiB: 4096, TTLSeconds: 900, NetworkProfile: "isolated-governed", CredentialLeases: []string{"repo-lease"},
	}
	return workspace.Workspace{
		ID: "workspace-pg", TenantID: "tenant-pg", ProjectID: spec.ProjectID, TaskID: spec.TaskID, Spec: spec,
		State: workspacev1.WorkspaceProvisioning, IdempotencyKey: "workspace-create-pg", RequestHash: "sha256:workspace-request",
		CorrelationID: "workspace-correlation-pg", ExpiresAt: now.Add(15 * time.Minute), CreatedBy: "agent-pg", UpdatedBy: "agent-pg",
		CreatedAt: now, UpdatedAt: now, Version: 1,
	}
}

func postgresCommand(id, idem string, now time.Time) workspace.Command {
	return workspace.Command{
		ID: id, TenantID: "tenant-pg", ProjectID: "project-pg", TaskID: "task-pg", WorkspaceID: "workspace-pg",
		Spec: workspacev1.CommandSpec{Argv: []string{"tofu", "apply", id + ".plan"}, WorkingDir: "infrastructure", TimeoutSeconds: 300, OutputLimitBytes: 4096},
		Kind: "infra_apply", SerializationKey: "staging", ActorID: "agent-pg", IdempotencyKey: idem, RequestHash: "sha256:" + id,
		State: workspacev1.CommandQueued, CreatedAt: now, UpdatedAt: now, Version: 1,
	}
}

func TestPostgres_WorkspaceIntentIsIdempotentDurableAndOutboxed(t *testing.T) {
	db, store := migratedWorkspaceStore(t)
	now := time.Date(2026, 7, 14, 7, 0, 0, 0, time.UTC)
	candidate := postgresWorkspace(now)
	created, inserted, err := store.CreateWorkspace(context.Background(), candidate)
	if err != nil || !inserted || created.ID != candidate.ID {
		t.Fatalf("create workspace: created=%#v inserted=%v err=%v", created, inserted, err)
	}
	replayed, inserted, err := store.CreateWorkspace(context.Background(), candidate)
	if err != nil || inserted || replayed.ID != candidate.ID {
		t.Fatalf("replay workspace: replayed=%#v inserted=%v err=%v", replayed, inserted, err)
	}
	conflict := candidate
	conflict.ID = "workspace-other"
	conflict.RequestHash = "sha256:different"
	if _, _, err := store.CreateWorkspace(context.Background(), conflict); !errors.Is(err, workspace.ErrConflict) {
		t.Fatalf("idempotency mismatch err=%v", err)
	}
	var pending int
	if err := db.QueryRow(`SELECT count(*) FROM workspace.outbox WHERE aggregate_id=$1 AND event_type='workspace.reconcile.requested' AND published_at IS NULL`, candidate.ID).Scan(&pending); err != nil || pending != 1 {
		t.Fatalf("durable outbox pending=%d err=%v", pending, err)
	}
	if _, err := db.Exec(`UPDATE workspace.outbox SET payload='{"tampered":true}' WHERE aggregate_id=$1`, candidate.ID); err == nil {
		t.Fatal("outbox payload mutation was accepted")
	}
	if _, err := db.Exec(`UPDATE workspace.outbox SET published_at=now() WHERE aggregate_id=$1`, candidate.ID); err != nil {
		t.Fatalf("first outbox delivery timestamp rejected: %v", err)
	}
	if _, err := db.Exec(`UPDATE workspace.outbox SET published_at=now() WHERE aggregate_id=$1`, candidate.ID); err == nil {
		t.Fatal("outbox delivery timestamp was mutable")
	}
}

func TestPostgres_WorkspaceStatefulCommandLockHasOneWinner(t *testing.T) {
	_, store := migratedWorkspaceStore(t)
	now := time.Date(2026, 7, 14, 7, 15, 0, 0, time.UTC)
	if _, _, err := store.CreateWorkspace(context.Background(), postgresWorkspace(now)); err != nil {
		t.Fatal(err)
	}
	const workers = 16
	commands := make([]workspace.Command, workers)
	for index := range commands {
		commands[index] = postgresCommand(fmt.Sprintf("command-%02d", index), fmt.Sprintf("command-idem-%02d", index), now)
		if _, _, err := store.CreateCommand(context.Background(), commands[index]); err != nil {
			t.Fatal(err)
		}
	}
	var winners atomic.Int64
	var group sync.WaitGroup
	errorsFound := make(chan error, workers)
	for index := range commands {
		command := commands[index]
		group.Add(1)
		go func() {
			defer group.Done()
			acquired, err := store.AcquireSerialization(context.Background(), command.ProjectID, command.SerializationKey, command.ID)
			if err != nil {
				errorsFound <- err
				return
			}
			if acquired {
				winners.Add(1)
			}
		}()
	}
	group.Wait()
	close(errorsFound)
	for err := range errorsFound {
		t.Fatal(err)
	}
	if winners.Load() != 1 {
		t.Fatalf("stateful serialization winners=%d want=1", winners.Load())
	}
}
