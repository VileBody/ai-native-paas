//go:build postgres_integration

package integration_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/keir-research/ai-native-paas/adapters/postgres/kernel"
	kernelv1 "github.com/keir-research/ai-native-paas/contracts/kernel/v1"
	"github.com/keir-research/ai-native-paas/internal/kernel"
)

var postgresTestMu sync.Mutex

func openPostgres(t *testing.T) *sql.DB {
	t.Helper()
	postgresTestMu.Lock()
	t.Cleanup(postgresTestMu.Unlock)
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not set; live PostgreSQL suite is not available")
	}
	db, err := sql.Open("kernel_libpq", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("connect to PostgreSQL: %v", err)
	}
	if _, err := db.ExecContext(ctx, `DROP SCHEMA IF EXISTS kernel CASCADE`); err != nil {
		t.Fatalf("reset kernel schema: %v", err)
	}
	return db
}

func migratedStore(t *testing.T) (*sql.DB, *kernelpostgres.Store) {
	t.Helper()
	db := openPostgres(t)
	if err := kernelpostgres.Migrate(context.Background(), db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	store, err := kernelpostgres.NewStore(db)
	if err != nil {
		t.Fatal(err)
	}
	return db, store
}

func TestPostgres_Migrations_CleanInstallAndUpgrade(t *testing.T) {
	db := openPostgres(t)
	ctx := context.Background()
	if err := kernelpostgres.Migrate(ctx, db); err != nil {
		t.Fatalf("clean migration: %v", err)
	}
	version, err := kernelpostgres.CurrentMigrationVersion(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	migrations, err := kernelpostgres.Migrations()
	if err != nil {
		t.Fatal(err)
	}
	if version != migrations[len(migrations)-1].Version {
		t.Fatalf("version = %d, want %d", version, migrations[len(migrations)-1].Version)
	}
	if err := kernelpostgres.Migrate(ctx, db); err != nil {
		t.Fatalf("idempotent upgrade: %v", err)
	}
	var applied int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM kernel.schema_migrations`).Scan(&applied); err != nil {
		t.Fatal(err)
	}
	if applied != len(migrations) {
		t.Fatalf("applied migrations = %d, want %d", applied, len(migrations))
	}
}

func TestPostgres_CreateOrganizationAndOutboxAreAtomic(t *testing.T) {
	db, store := migratedStore(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
	organization, err := kernel.NewOrganization("org_atomic", "Atomic", "owner", now)
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(map[string]any{"organization_id": organization.ID})
	event := kernelv1.DomainEventEnvelope[json.RawMessage]{
		EventID: "evt_atomic", Type: kernel.EventOrganizationCreated, Version: 1,
		TenantID: organization.ID, AggregateID: string(organization.ID),
		CorrelationID: "cor_atomic", OccurredAt: now, Payload: payload,
	}
	sentinel := errors.New("force rollback")
	err = store.Transact(ctx, func(tx kernel.Tx) error {
		if err := tx.InsertOrganization(ctx, organization); err != nil {
			return err
		}
		if err := tx.AppendOutbox(ctx, kernel.OutboxRecord{Event: event, State: kernel.OutboxPending, CreatedAt: now}); err != nil {
			return err
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("rollback error = %v", err)
	}
	for _, table := range []string{"organizations", "outbox_events"} {
		var count int
		if err := db.QueryRowContext(ctx, `SELECT count(*) FROM kernel.`+table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("%s count after rollback = %d", table, count)
		}
	}
	if err := store.Transact(ctx, func(tx kernel.Tx) error {
		if err := tx.InsertOrganization(ctx, organization); err != nil {
			return err
		}
		return tx.AppendOutbox(ctx, kernel.OutboxRecord{Event: event, State: kernel.OutboxPending, CreatedAt: now})
	}); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"organizations", "outbox_events"} {
		var count int
		if err := db.QueryRowContext(ctx, `SELECT count(*) FROM kernel.`+table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("%s count after commit = %d", table, count)
		}
	}
}

func TestKernel_OutboxCommitThenCrashPublishesExactlyOnceEffect(t *testing.T) {
	db, store := migratedStore(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	organization, err := kernel.NewOrganization("org_outbox_crash", "Outbox crash", "owner", now)
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(map[string]any{"organization_id": organization.ID})
	event := kernelv1.DomainEventEnvelope[json.RawMessage]{
		EventID: "evt_outbox_crash", Type: kernel.EventOrganizationCreated, Version: 1,
		TenantID: organization.ID, AggregateID: string(organization.ID),
		CorrelationID: "cor_outbox_crash", OccurredAt: now, Payload: payload,
	}
	if err := store.Transact(ctx, func(tx kernel.Tx) error {
		if err := tx.InsertOrganization(ctx, organization); err != nil {
			return err
		}
		return tx.AppendOutbox(ctx, kernel.OutboxRecord{Event: event, State: kernel.OutboxPending, CreatedAt: now})
	}); err != nil {
		t.Fatal(err)
	}

	// The process that committed the aggregate is gone. A restarted dispatcher
	// delivers the durable event, then loses its database acknowledgement.
	clock := kernel.NewFixedClock(now)
	broker := &postgresInboxBroker{processor: kernel.InboxProcessor{Store: store, Clock: clock}}
	crashingStore := &failKernelMarkPublishedStore{Store: store}
	crashingStore.failNext.Store(true)
	dispatcher := kernel.OutboxDispatcher{Store: crashingStore, Publisher: broker, Clock: clock, Lease: time.Second}
	if count, err := dispatcher.Dispatch(ctx, 1); err == nil || count != 0 {
		t.Fatalf("dispatch before simulated crash = %d, %v", count, err)
	}

	// A second process takes over the expired lease and redelivers. The
	// PostgreSQL inbox turns at-least-once transport into exactly-once effect.
	clock.Advance(2 * time.Second)
	restarted := kernel.OutboxDispatcher{Store: store, Publisher: broker, Clock: clock, Lease: time.Second}
	if count, err := restarted.Dispatch(ctx, 1); err != nil || count != 1 {
		t.Fatalf("restart dispatch = %d, %v", count, err)
	}
	if broker.deliveries.Load() != 2 || broker.effects.Load() != 1 {
		t.Fatalf("deliveries=%d effects=%d", broker.deliveries.Load(), broker.effects.Load())
	}
	for query, want := range map[string]int{
		`SELECT count(*) FROM kernel.inbox_events WHERE event_id='evt_outbox_crash'`:                               1,
		`SELECT count(*) FROM kernel.outbox_events WHERE event_id='evt_outbox_crash' AND state='PUBLISHED'`:        1,
		`SELECT count(*) FROM kernel.organizations WHERE id='org_outbox_crash'`:                                    1,
		`SELECT count(*) FROM kernel.outbox_events WHERE event_id='evt_outbox_crash' AND published_at IS NOT NULL`: 1,
	} {
		var got int
		if err := db.QueryRowContext(ctx, query).Scan(&got); err != nil || got != want {
			t.Fatalf("query=%q got=%d want=%d err=%v", query, got, want, err)
		}
	}
}

type postgresInboxBroker struct {
	processor  kernel.InboxProcessor
	deliveries atomic.Int32
	effects    atomic.Int32
}

func (b *postgresInboxBroker) Publish(ctx context.Context, event kernelv1.DomainEventEnvelope[json.RawMessage]) error {
	b.deliveries.Add(1)
	_, err := b.processor.Process(ctx, event, func(context.Context, kernel.Tx, kernelv1.DomainEventEnvelope[json.RawMessage]) error {
		b.effects.Add(1)
		return nil
	})
	return err
}

type failKernelMarkPublishedStore struct {
	kernel.Store
	failNext atomic.Bool
}

func (s *failKernelMarkPublishedStore) Transact(ctx context.Context, fn func(kernel.Tx) error) error {
	return s.Store.Transact(ctx, func(tx kernel.Tx) error {
		return fn(&failKernelMarkPublishedTx{Tx: tx, parent: s})
	})
}

type failKernelMarkPublishedTx struct {
	kernel.Tx
	parent *failKernelMarkPublishedStore
}

func (tx *failKernelMarkPublishedTx) MarkOutboxPublished(ctx context.Context, eventID string, now time.Time) error {
	if tx.parent.failNext.CompareAndSwap(true, false) {
		return errors.New("simulated crash after broker delivery")
	}
	return tx.Tx.MarkOutboxPublished(ctx, eventID, now)
}

func TestPostgres_ConcurrentIdempotencyUsesSingleWinner(t *testing.T) {
	_, store := migratedStore(t)
	service, err := kernel.NewService(store, kernel.SystemClock{}, kernel.CryptoIDGenerator{})
	if err != nil {
		t.Fatal(err)
	}
	command := kernel.CreateOrganizationCommand{
		Meta: kernelv1.CommandMeta{
			Principal:     kernelv1.PrincipalContext{PrincipalID: "owner", Kind: kernelv1.PrincipalKindUser, Scopes: []string{"kernel:*"}},
			CorrelationID: "cor_pg_concurrent", IdempotencyKey: "same-key",
		},
		Name: "Concurrent PostgreSQL",
	}
	const workers = 24
	var group sync.WaitGroup
	results := make(chan kernel.CreateOrganizationResult, workers)
	errorsCh := make(chan error, workers)
	group.Add(workers)
	for i := 0; i < workers; i++ {
		go func() {
			defer group.Done()
			result, err := service.CreateOrganization(context.Background(), command)
			if err != nil {
				errorsCh <- err
				return
			}
			results <- result
		}()
	}
	group.Wait()
	close(results)
	close(errorsCh)
	for err := range errorsCh {
		t.Errorf("concurrent request: %v", err)
	}
	organizations := map[kernelv1.TenantID]struct{}{}
	operations := map[kernelv1.OperationID]struct{}{}
	for result := range results {
		organizations[result.Organization.OrganizationID] = struct{}{}
		operations[result.Operation.OperationID] = struct{}{}
	}
	if len(organizations) != 1 || len(operations) != 1 {
		t.Fatalf("organizations=%d operations=%d", len(organizations), len(operations))
	}
}

func TestPostgres_OperationOptimisticLockPreventsLostUpdate(t *testing.T) {
	_, store := migratedStore(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
	operation, err := kernel.NewOperation("op_lock", "org_1", "test", "cor_lock", "", now)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Transact(ctx, func(tx kernel.Tx) error { return tx.InsertOperation(ctx, operation) }); err != nil {
		t.Fatal(err)
	}
	var first, second *kernel.Operation
	if err := store.View(ctx, func(reader kernel.Reader) error {
		var err error
		first, err = reader.GetOperation(ctx, operation.ID)
		if err != nil {
			return err
		}
		second, err = reader.GetOperation(ctx, operation.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := first.Transition(kernelv1.OperationRunning, kernelv1.OperationResult{Code: "RUNNING"}, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := second.Transition(kernelv1.OperationFailed, kernelv1.OperationResult{Code: "FAILED", Error: &kernelv1.PublicError{Code: "TEST_FAILURE", Message: "failed"}}, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := store.Transact(ctx, func(tx kernel.Tx) error { return tx.SaveOperation(ctx, first, 1) }); err != nil {
		t.Fatal(err)
	}
	err = store.Transact(ctx, func(tx kernel.Tx) error { return tx.SaveOperation(ctx, second, 1) })
	if kernel.ErrorCode(err) != kernelv1.CodeOptimisticLock {
		t.Fatalf("second save error = %v, want OPTIMISTIC_LOCK_CONFLICT", err)
	}
}

func TestPostgres_AuditTableRejectsUpdateAndDelete(t *testing.T) {
	db, store := migratedStore(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
	record := kernelv1.AuditEnvelope{
		AuditID: "aud_immutable", TenantID: "org_1",
		Actor:         kernelv1.PrincipalContext{PrincipalID: "owner", Kind: kernelv1.PrincipalKindUser, Scopes: []string{"kernel:*"}},
		Action:        kernel.ActionOrganizationRead,
		Resource:      kernelv1.ResourceRef{TenantID: "org_1", Type: "organization", ID: "org_1"},
		CorrelationID: "cor_audit", Outcome: kernelv1.AuditOutcomeSucceeded, OccurredAt: now,
	}
	if err := store.Transact(ctx, func(tx kernel.Tx) error { return tx.AppendAudit(ctx, record) }); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE kernel.audit_records SET action = 'tampered' WHERE audit_id = 'aud_immutable'`); err == nil {
		t.Fatal("audit UPDATE unexpectedly succeeded")
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM kernel.audit_records WHERE audit_id = 'aud_immutable'`); err == nil {
		t.Fatal("audit DELETE unexpectedly succeeded")
	}
	var count int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM kernel.audit_records WHERE audit_id = 'aud_immutable'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("audit record count = %d", count)
	}
}
