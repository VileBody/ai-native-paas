package kernel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	kernelv1 "github.com/keir-research/ai-native-paas/contracts/kernel/v1"
)

type OutboxDispatcher struct {
	Store     Store
	Publisher kernelv1.EventPublisher
	Clock     Clock
	Lease     time.Duration
}

func (d OutboxDispatcher) Dispatch(ctx context.Context, limit int) (int, error) {
	if d.Store == nil || d.Publisher == nil || d.Clock == nil {
		return 0, errors.New("outbox dispatcher requires store, publisher, and clock")
	}
	if limit <= 0 {
		return 0, nil
	}
	lease := d.Lease
	if lease <= 0 {
		lease = 30 * time.Second
	}
	var claimed []OutboxRecord
	if err := d.Store.Transact(ctx, func(tx Tx) error {
		records, err := tx.ClaimOutbox(ctx, limit, d.Clock.Now(), lease)
		claimed = records
		return err
	}); err != nil {
		return 0, err
	}

	published := 0
	var dispatchErrors []error
	for _, record := range claimed {
		if err := d.Publisher.Publish(ctx, record.Event); err != nil {
			dispatchErrors = append(dispatchErrors, fmt.Errorf("publish %s: %w", record.Event.EventID, err))
			markErr := d.Store.Transact(ctx, func(tx Tx) error {
				return tx.MarkOutboxPending(ctx, record.Event.EventID, err.Error(), d.Clock.Now())
			})
			if markErr != nil {
				dispatchErrors = append(dispatchErrors, fmt.Errorf("release %s: %w", record.Event.EventID, markErr))
			}
			continue
		}
		if err := d.Store.Transact(ctx, func(tx Tx) error {
			return tx.MarkOutboxPublished(ctx, record.Event.EventID, d.Clock.Now())
		}); err != nil {
			// At-least-once semantics: a crash/failure after publish leaves the
			// record retryable, so downstream consumers must use the inbox.
			dispatchErrors = append(dispatchErrors, fmt.Errorf("mark published %s: %w", record.Event.EventID, err))
			continue
		}
		published++
	}
	return published, errors.Join(dispatchErrors...)
}

type InboxHandler func(ctx context.Context, tx Tx, event kernelv1.DomainEventEnvelope[json.RawMessage]) error

type InboxResult struct {
	Duplicate bool
}

type InboxProcessor struct {
	Store Store
	Clock Clock
}

func (p InboxProcessor) Process(ctx context.Context, event kernelv1.DomainEventEnvelope[json.RawMessage], handler InboxHandler) (InboxResult, error) {
	if p.Store == nil || p.Clock == nil || handler == nil {
		return InboxResult{}, errors.New("inbox processor requires store, clock, and handler")
	}
	if err := event.Validate(); err != nil {
		return InboxResult{}, invalidArgument("invalid event envelope: %v", err)
	}
	fingerprint, err := CanonicalFingerprint(event)
	if err != nil {
		return InboxResult{}, err
	}

	result := InboxResult{}
	err = p.Store.Transact(ctx, func(tx Tx) error {
		existing, getErr := tx.GetInbox(ctx, event.EventID)
		if getErr == nil {
			if existing.Fingerprint != fingerprint {
				return NewError(kernelv1.CodeConflict, "event id was already processed with a different payload")
			}
			result.Duplicate = true
			return nil
		}
		if ErrorCode(getErr) != kernelv1.CodeNotFound {
			return getErr
		}
		if err := handler(ctx, tx, event); err != nil {
			return err
		}
		return tx.InsertInbox(ctx, InboxRecord{EventID: event.EventID, Fingerprint: fingerprint, ProcessedAt: p.Clock.Now()})
	})
	return result, err
}
