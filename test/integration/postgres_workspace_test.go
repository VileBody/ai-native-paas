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
	"github.com/keir-research/ai-native-paas/internal/workspace/session"
	sessionpostgres "github.com/keir-research/ai-native-paas/internal/workspace/session/postgres"
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

func TestPostgres_WorkspaceOutboxLeaseHasOneWinnerAndCrashTakeover(t *testing.T) {
	db, store := migratedWorkspaceStore(t)
	now := time.Date(2026, 7, 14, 7, 10, 0, 0, time.UTC)
	candidate := postgresWorkspace(now)
	if _, _, err := store.CreateWorkspace(context.Background(), candidate); err != nil {
		t.Fatal(err)
	}

	const workers = 12
	start := make(chan struct{})
	type claimResult struct {
		owner   string
		records []workspace.OutboxRecord
		err     error
	}
	results := make(chan claimResult, workers)
	for index := 0; index < workers; index++ {
		owner := fmt.Sprintf("outbox-worker-%02d", index)
		go func() {
			<-start
			records, err := store.ClaimOutbox(context.Background(), owner, now, now.Add(time.Minute), 1)
			results <- claimResult{owner: owner, records: records, err: err}
		}()
	}
	close(start)
	var winner claimResult
	claimed := 0
	for index := 0; index < workers; index++ {
		result := <-results
		if result.err != nil {
			t.Fatal(result.err)
		}
		claimed += len(result.records)
		if len(result.records) == 1 {
			winner = result
		}
	}
	if claimed != 1 || winner.records[0].AggregateID != candidate.ID || winner.records[0].DeliveryAttempts != 1 {
		t.Fatalf("concurrent claims=%d winner=%#v", claimed, winner)
	}
	if _, err := db.Exec(`UPDATE workspace.outbox SET payload='{"tampered":true}' WHERE event_id=$1`, winner.records[0].EventID); err == nil {
		t.Fatal("leased outbox payload mutation was accepted")
	}

	retryAt := now.Add(10 * time.Second)
	if err := store.DeferOutbox(context.Background(), winner.records[0].EventID, winner.owner, retryAt); err != nil {
		t.Fatal(err)
	}
	before, err := store.ClaimOutbox(context.Background(), "takeover-worker", retryAt.Add(-time.Millisecond), retryAt.Add(time.Minute), 1)
	if err != nil || len(before) != 0 {
		t.Fatalf("unexpired lease takeover records=%#v err=%v", before, err)
	}
	after, err := store.ClaimOutbox(context.Background(), "takeover-worker", retryAt, retryAt.Add(time.Minute), 1)
	if err != nil || len(after) != 1 || after[0].DeliveryAttempts != 2 {
		t.Fatalf("expired lease takeover records=%#v err=%v", after, err)
	}
	if err := store.MarkOutboxPublished(context.Background(), after[0].EventID, winner.owner, retryAt); !errors.Is(err, workspace.ErrConflict) {
		t.Fatalf("stale owner acknowledged event: %v", err)
	}
	if err := store.MarkOutboxPublished(context.Background(), after[0].EventID, "takeover-worker", retryAt); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkOutboxPublished(context.Background(), after[0].EventID, "takeover-worker", retryAt); !errors.Is(err, workspace.ErrConflict) {
		t.Fatalf("duplicate acknowledgement accepted: %v", err)
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

type workspaceSessionClock struct{ now time.Time }

func (c workspaceSessionClock) Now() time.Time { return c.now }

type workspaceSessionIDs struct{ next atomic.Int64 }

func (i *workspaceSessionIDs) New(prefix string) string {
	return fmt.Sprintf("%s-pg-%d", prefix, i.next.Add(1))
}

func TestPostgres_WorkspaceAgentAckSurvivesControlPlaneRestart(t *testing.T) {
	db, workspaceStore := migratedWorkspaceStore(t)
	now := time.Date(2026, 7, 14, 8, 0, 0, 0, time.UTC)
	if _, _, err := workspaceStore.CreateWorkspace(context.Background(), postgresWorkspace(now)); err != nil {
		t.Fatal(err)
	}
	ids := &workspaceSessionIDs{}
	clock := workspaceSessionClock{now: now}
	sessionStore := &sessionpostgres.Store{DB: db}
	registry := &session.Registry{Store: sessionStore, Clock: clock, IDs: ids, AckPollInterval: time.Millisecond, DispatchAckTimeout: 2 * time.Second}
	principal := session.Principal{
		TenantID: "tenant-pg", ProjectID: "project-pg", WorkspaceID: "workspace-pg", TaskID: "task-pg",
		AgentID: "agent-pg", CertificateID: "certificate-pg", NotAfter: now.Add(10 * time.Minute),
	}
	connected, err := registry.Connect(context.Background(), principal, "vm-pg")
	if err != nil {
		t.Fatal(err)
	}
	envelope := workspace.CommandEnvelope{
		CommandID: "command-pg", WorkspaceID: "workspace-pg", ProjectID: "project-pg", TaskID: "task-pg",
		Spec: workspacev1.CommandSpec{Argv: []string{"tofu", "plan"}, WorkingDir: "infrastructure", TimeoutSeconds: 60, OutputLimitBytes: 4096},
	}
	type dispatchResult struct {
		receipt workspace.DispatchReceipt
		err     error
	}
	done := make(chan dispatchResult, 1)
	go func() {
		receipt, dispatchErr := registry.Dispatch(context.Background(), envelope)
		done <- dispatchResult{receipt: receipt, err: dispatchErr}
	}()
	var message workspacev1.AgentMessage
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		message, err = registry.Next(context.Background(), principal, connected.SessionID)
		if err == nil {
			break
		}
		if !errors.Is(err, session.ErrNotFound) {
			t.Fatal(err)
		}
		time.Sleep(time.Millisecond)
	}
	if err != nil {
		t.Fatalf("claim outbound message: %v", err)
	}
	if err := registry.Acknowledge(context.Background(), principal, connected.SessionID, message.MessageID, true); err != nil {
		t.Fatal(err)
	}
	first := <-done
	if first.err != nil || !first.receipt.Accepted || first.receipt.VMID != "vm-pg" {
		t.Fatalf("dispatch receipt=%#v err=%v", first.receipt, first.err)
	}

	restarted := &session.Registry{Store: &sessionpostgres.Store{DB: db}, Clock: clock, IDs: ids, AckPollInterval: time.Millisecond, DispatchAckTimeout: time.Second}
	replayed, err := restarted.Dispatch(context.Background(), envelope)
	if err != nil || replayed != first.receipt {
		t.Fatalf("restart replay receipt=%#v err=%v", replayed, err)
	}
	var messageCount int
	if err := db.QueryRow(`SELECT count(*) FROM workspace.agent_messages WHERE workspace_id=$1 AND command_id=$2`, "workspace-pg", "command-pg").Scan(&messageCount); err != nil || messageCount != 1 {
		t.Fatalf("durable message count=%d err=%v", messageCount, err)
	}
}
