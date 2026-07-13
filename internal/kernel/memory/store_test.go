package memory

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	kernelv1 "github.com/keir-research/ai-native-paas/contracts/kernel/v1"
	"github.com/keir-research/ai-native-paas/internal/kernel"
)

var now = time.Date(2026, 7, 12, 13, 0, 0, 0, time.UTC)

func TestStore_ViewReturnsDetachedAggregate(t *testing.T) {
	store := NewStore()
	organization, _ := kernel.NewOrganization("org_1", "Acme", "owner", now)
	if err := store.Transact(context.Background(), func(tx kernel.Tx) error { return tx.InsertOrganization(context.Background(), organization) }); err != nil {
		t.Fatal(err)
	}
	var fetched *kernel.Organization
	if err := store.View(context.Background(), func(reader kernel.Reader) error {
		var err error
		fetched, err = reader.GetOrganization(context.Background(), "org_1")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	fetched.Name = "Mutated outside transaction"
	fetched.Memberships["owner"].Role = kernel.RoleViewer

	if err := store.View(context.Background(), func(reader kernel.Reader) error {
		persisted, err := reader.GetOrganization(context.Background(), "org_1")
		if err != nil {
			return err
		}
		if persisted.Name != "Acme" || persisted.Memberships["owner"].Role != kernel.RoleOwner {
			t.Fatalf("detached mutation leaked: %+v", persisted)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestStore_OrganizationOptimisticLockPreventsLostUpdate(t *testing.T) {
	store := NewStore()
	organization, _ := kernel.NewOrganization("org_1", "Acme", "owner", now)
	_ = store.Transact(context.Background(), func(tx kernel.Tx) error { return tx.InsertOrganization(context.Background(), organization) })

	var first, second *kernel.Organization
	_ = store.View(context.Background(), func(reader kernel.Reader) error {
		first, _ = reader.GetOrganization(context.Background(), "org_1")
		second, _ = reader.GetOrganization(context.Background(), "org_1")
		return nil
	})
	expected := first.Version
	_ = first.Invite("dev_1", kernel.RoleDeveloper, now)
	if err := store.Transact(context.Background(), func(tx kernel.Tx) error { return tx.SaveOrganization(context.Background(), first, expected) }); err != nil {
		t.Fatal(err)
	}
	_ = second.Invite("dev_2", kernel.RoleDeveloper, now)
	err := store.Transact(context.Background(), func(tx kernel.Tx) error { return tx.SaveOrganization(context.Background(), second, expected) })
	if kernel.ErrorCode(err) != kernelv1.CodeOptimisticLock {
		t.Fatalf("error = %v, want OPTIMISTIC_LOCK_CONFLICT", err)
	}
}

func TestStore_OperationOptimisticLockPreventsLostUpdate(t *testing.T) {
	store := NewStore()
	operation, _ := kernel.NewOperation("op_1", "org_1", "test", "cor_1", "", now)
	_ = store.Transact(context.Background(), func(tx kernel.Tx) error { return tx.InsertOperation(context.Background(), operation) })
	var first, second *kernel.Operation
	_ = store.View(context.Background(), func(reader kernel.Reader) error {
		first, _ = reader.GetOperation(context.Background(), "op_1")
		second, _ = reader.GetOperation(context.Background(), "op_1")
		return nil
	})
	expected := first.Version
	_ = first.Transition(kernelv1.OperationRunning, kernelv1.OperationResult{}, now)
	if err := store.Transact(context.Background(), func(tx kernel.Tx) error { return tx.SaveOperation(context.Background(), first, expected) }); err != nil {
		t.Fatal(err)
	}
	_ = second.Transition(kernelv1.OperationRunning, kernelv1.OperationResult{}, now)
	err := store.Transact(context.Background(), func(tx kernel.Tx) error { return tx.SaveOperation(context.Background(), second, expected) })
	if kernel.ErrorCode(err) != kernelv1.CodeOptimisticLock {
		t.Fatalf("error = %v, want OPTIMISTIC_LOCK_CONFLICT", err)
	}
}

func TestStore_TransactionRollsBackAllWrites(t *testing.T) {
	store := NewStore()
	sentinel := errors.New("rollback")
	organization, _ := kernel.NewOrganization("org_1", "Acme", "owner", now)
	err := store.Transact(context.Background(), func(tx kernel.Tx) error {
		if err := tx.InsertOrganization(context.Background(), organization); err != nil {
			return err
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("error = %v", err)
	}
	_ = store.View(context.Background(), func(reader kernel.Reader) error {
		_, err := reader.GetOrganization(context.Background(), "org_1")
		if kernel.ErrorCode(err) != kernelv1.CodeNotFound {
			t.Fatalf("organization persisted after rollback: %v", err)
		}
		return nil
	})
}

func TestStore_ContextCancellationPreventsCommit(t *testing.T) {
	store := NewStore()
	ctx, cancel := context.WithCancel(context.Background())
	organization, _ := kernel.NewOrganization("org_1", "Acme", "owner", now)
	err := store.Transact(ctx, func(tx kernel.Tx) error {
		if err := tx.InsertOrganization(ctx, organization); err != nil {
			return err
		}
		cancel()
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	_ = store.View(context.Background(), func(reader kernel.Reader) error {
		_, err := reader.GetOrganization(context.Background(), "org_1")
		if kernel.ErrorCode(err) != kernelv1.CodeNotFound {
			t.Fatalf("organization persisted after canceled transaction: %v", err)
		}
		return nil
	})
}

func TestStore_EnforcesUniqueOrganizationSlug(t *testing.T) {
	store := NewStore()
	first, _ := kernel.NewOrganization("org_1", "Acme", "owner_1", now)
	second, _ := kernel.NewOrganization("org_2", "ACME", "owner_2", now)
	if err := store.Transact(context.Background(), func(tx kernel.Tx) error { return tx.InsertOrganization(context.Background(), first) }); err != nil {
		t.Fatal(err)
	}
	err := store.Transact(context.Background(), func(tx kernel.Tx) error { return tx.InsertOrganization(context.Background(), second) })
	if kernel.ErrorCode(err) != kernelv1.CodeConflict {
		t.Fatalf("error = %v, want CONFLICT", err)
	}
}

func TestStore_CompleteIdempotencyIsIdempotentForIdenticalResponse(t *testing.T) {
	store := NewStore()
	response := json.RawMessage(`{"operation":{"operation_id":"op_1"}}`)
	if err := store.Transact(context.Background(), func(tx kernel.Tx) error {
		_, claimed, err := tx.ClaimIdempotency(context.Background(), kernel.IdempotencyRecord{Scope: "scope", Key: "key", Fingerprint: "fp", CreatedAt: now, UpdatedAt: now})
		if err != nil || !claimed {
			t.Fatalf("claim = %v, %v", claimed, err)
		}
		if err := tx.CompleteIdempotency(context.Background(), "scope", "key", "op_1", response, now); err != nil {
			return err
		}
		return tx.CompleteIdempotency(context.Background(), "scope", "key", "op_1", response, now)
	}); err != nil {
		t.Fatal(err)
	}
}

func TestStore_OutboxClaimDoesNotReturnPublishedEvent(t *testing.T) {
	store := NewStore()
	event := kernelv1.DomainEventEnvelope[json.RawMessage]{EventID: "evt_1", Type: "test.v1", Version: 1, AggregateID: "a", CorrelationID: "c", OccurredAt: now, Payload: json.RawMessage(`{}`)}
	if err := store.Transact(context.Background(), func(tx kernel.Tx) error {
		if err := tx.AppendOutbox(context.Background(), kernel.OutboxRecord{Event: event, State: kernel.OutboxPending, CreatedAt: now}); err != nil {
			return err
		}
		records, err := tx.ClaimOutbox(context.Background(), 1, now, time.Minute)
		if err != nil || len(records) != 1 {
			t.Fatalf("claim = %+v, %v", records, err)
		}
		return tx.MarkOutboxPublished(context.Background(), "evt_1", now)
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Transact(context.Background(), func(tx kernel.Tx) error {
		records, err := tx.ClaimOutbox(context.Background(), 1, now.Add(time.Hour), time.Minute)
		if err != nil {
			return err
		}
		if len(records) != 0 {
			t.Fatalf("published event was reclaimed: %+v", records)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
