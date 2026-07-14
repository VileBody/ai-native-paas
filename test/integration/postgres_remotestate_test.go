//go:build postgres_integration

package integration_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/keir-research/ai-native-paas/internal/remotestate"
)

type remoteStateBlobStore struct {
	mu       sync.Mutex
	putCount int
	blob     remotestate.Blob
}

func (s *remoteStateBlobStore) Get(context.Context, string) (remotestate.Blob, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.putCount == 0 {
		return remotestate.Blob{}, remotestate.ErrNotFound
	}
	result := s.blob
	result.Data = append([]byte(nil), result.Data...)
	return result, nil
}

func (s *remoteStateBlobStore) Put(_ context.Context, _ string, data []byte) (remotestate.Blob, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.putCount++
	s.blob = remotestate.Blob{Data: append([]byte(nil), data...), VersionID: "version-1", ETag: remotestate.Digest(data)}
	return s.blob, nil
}

func (*remoteStateBlobStore) Delete(context.Context, string) error { return nil }

func (s *remoteStateBlobStore) Puts() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.putCount
}

func TestInfraState_RemoteLockPreventsConcurrentMutation(t *testing.T) {
	db := openPostgres(t)
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `DROP SCHEMA IF EXISTS state_service CASCADE`); err != nil {
		t.Fatalf("reset state service schema: %v", err)
	}
	repository, err := remotestate.NewPostgresRepository(db, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.Migrate(ctx); err != nil {
		t.Fatalf("migrate state service: %v", err)
	}
	const namespace = "tenant-1.project-1.production"
	if err := repository.EnsureNamespace(ctx, remotestate.NamespaceOwner{
		Namespace: namespace,
		TenantID:  "tenant-1",
		ProjectID: "project-1",
	}); err != nil {
		t.Fatalf("ensure namespace: %v", err)
	}
	blobs := &remoteStateBlobStore{}
	service, err := remotestate.NewService(blobs, repository)
	if err != nil {
		t.Fatal(err)
	}

	type result struct {
		requested remotestate.Lock
		existing  *remotestate.Lock
		err       error
	}
	start := make(chan struct{})
	results := make(chan result, 2)
	var workers sync.WaitGroup
	for _, lock := range []remotestate.Lock{
		{ID: "workspace-1-lock", Operation: "OperationTypeApply", Who: "workspace-1"},
		{ID: "workspace-2-lock", Operation: "OperationTypeApply", Who: "workspace-2"},
	} {
		workers.Add(1)
		go func(requested remotestate.Lock) {
			defer workers.Done()
			<-start
			existing, acquireErr := service.Acquire(ctx, namespace, requested.Who, requested)
			results <- result{requested: requested, existing: existing, err: acquireErr}
		}(lock)
	}
	close(start)
	workers.Wait()
	close(results)

	var winner, loser result
	for candidate := range results {
		switch {
		case candidate.err == nil:
			winner = candidate
		case errors.Is(candidate.err, remotestate.ErrLocked):
			loser = candidate
		default:
			t.Fatalf("unexpected lock result for %s: %v", candidate.requested.ID, candidate.err)
		}
	}
	if winner.requested.ID == "" || loser.requested.ID == "" {
		t.Fatalf("expected one winner and one conflict: winner=%+v loser=%+v", winner, loser)
	}
	if loser.existing == nil || loser.existing.ID != winner.requested.ID || loser.existing.Who != winner.requested.Who {
		t.Fatalf("contender did not receive visible lock owner: winner=%+v existing=%+v", winner.requested, loser.existing)
	}
	encryptedState := []byte(`{"encryption_version":"v0","encrypted_data":"winner-ciphertext","meta":{"key_provider.pbkdf2.offline_recovery":"opaque"}}`)
	if _, err := service.Put(ctx, namespace, loser.requested.ID, loser.requested.Who, encryptedState); !errors.Is(err, remotestate.ErrLockMismatch) {
		t.Fatalf("losing workspace mutation error=%v", err)
	}
	if _, err := service.Put(ctx, namespace, winner.requested.ID, winner.requested.Who, encryptedState); err != nil {
		t.Fatalf("winning workspace mutation: %v", err)
	}
	if blobs.Puts() != 1 {
		t.Fatalf("blob mutations=%d, want exactly one", blobs.Puts())
	}

	if err := service.RecoverStale(ctx, namespace, winner.requested.ID, remotestate.RecoveryAuthorization{
		Allowed: true,
		Actor:   "operator-1",
		Reason:  "premature recovery must fail",
	}); !errors.Is(err, remotestate.ErrLocked) {
		t.Fatalf("active lock recovery error=%v, want locked", err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE state_service.locks SET lease_expires_at=now() - interval '1 second' WHERE namespace=$1`, namespace); err != nil {
		t.Fatalf("expire test lease: %v", err)
	}
	staleOwner, err := service.Acquire(ctx, namespace, "workspace-3", remotestate.Lock{ID: "workspace-3-lock", Who: "workspace-3"})
	if !errors.Is(err, remotestate.ErrStaleLock) || staleOwner == nil || staleOwner.ID != winner.requested.ID {
		t.Fatalf("stale acquire owner=%+v error=%v", staleOwner, err)
	}
	if err := service.RecoverStale(ctx, namespace, winner.requested.ID, remotestate.RecoveryAuthorization{
		Actor:  "workspace-3",
		Reason: "agent attempted unaudited force unlock",
	}); !errors.Is(err, remotestate.ErrRecoveryForbidden) {
		t.Fatalf("unprivileged recovery error=%v", err)
	}
	const recoveryReason = "workspace heartbeat expired after confirmed provider timeout"
	if err := service.RecoverStale(ctx, namespace, winner.requested.ID, remotestate.RecoveryAuthorization{
		Allowed: true,
		Actor:   "operator-1",
		Reason:  recoveryReason,
	}); err != nil {
		t.Fatalf("recover stale lock: %v", err)
	}
	var recoveredLockID, recoveredBy, reason string
	if err := db.QueryRowContext(ctx, `
SELECT lock_id, recovered_by, reason
FROM state_service.lock_recoveries
WHERE namespace=$1`, namespace).Scan(&recoveredLockID, &recoveredBy, &reason); err != nil {
		t.Fatalf("read recovery audit: %v", err)
	}
	if recoveredLockID != winner.requested.ID || recoveredBy != "operator-1" || reason != recoveryReason {
		t.Fatalf("recovery audit lock=%q actor=%q reason=%q", recoveredLockID, recoveredBy, reason)
	}
	if existing, err := service.Acquire(ctx, namespace, "workspace-3", remotestate.Lock{ID: "workspace-3-lock", Who: "workspace-3"}); err != nil || existing != nil {
		t.Fatalf("lock after audited recovery existing=%+v error=%v", existing, err)
	}
}
