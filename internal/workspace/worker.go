package workspace

import (
	"context"
	"errors"
	"strings"
	"time"

	workspacev1 "github.com/keir-research/ai-native-paas/pkg/contracts/workspace/v1"
)

var ErrOutboxPending = errors.New("workspace outbox effect is waiting for external readiness")

type OutboxRecord struct {
	EventID          int64
	AggregateType    string
	AggregateID      string
	AggregateVersion int64
	EventType        string
	DeliveryAttempts int64
}

type WorkerStore interface {
	ClaimOutbox(context.Context, string, time.Time, time.Time, int) ([]OutboxRecord, error)
	DeferOutbox(context.Context, int64, string, time.Time) error
	MarkOutboxPublished(context.Context, int64, string, time.Time) error
	GetCommandForWorker(context.Context, string) (Command, error)
}

type WorkspaceActions interface {
	Reconcile(context.Context, string) (workspacev1.WorkspaceRef, error)
	Dispatch(context.Context, Scope, string) (workspacev1.CommandView, error)
}

type Worker struct {
	Store       WorkerStore
	Actions     WorkspaceActions
	Clock       Clock
	WorkerID    string
	Lease       time.Duration
	RetryDelay  time.Duration
	AfterEffect func(OutboxRecord) error
}

func (w *Worker) RunOnce(ctx context.Context, limit int) (int, error) {
	if w == nil || w.Store == nil || w.Actions == nil || w.Clock == nil || strings.TrimSpace(w.WorkerID) == "" || limit < 1 || limit > 1000 {
		return 0, errors.New("workspace worker configuration is invalid")
	}
	lease := w.Lease
	if lease <= 0 {
		lease = 10 * time.Minute
	}
	retryDelay := w.RetryDelay
	if retryDelay <= 0 {
		retryDelay = 5 * time.Second
	}
	now := w.Clock.Now().UTC()
	records, err := w.Store.ClaimOutbox(ctx, w.WorkerID, now, now.Add(lease), limit)
	if err != nil {
		return 0, err
	}
	published := 0
	var failures []error
	for _, record := range records {
		if err := w.handle(ctx, record); err != nil {
			if deferErr := w.Store.DeferOutbox(ctx, record.EventID, w.WorkerID, w.Clock.Now().UTC().Add(retryDelay)); deferErr != nil {
				failures = append(failures, deferErr)
			} else if !errors.Is(err, ErrOutboxPending) {
				failures = append(failures, err)
			}
			continue
		}
		if w.AfterEffect != nil {
			if err := w.AfterEffect(record); err != nil {
				return published, err
			}
		}
		if err := w.Store.MarkOutboxPublished(ctx, record.EventID, w.WorkerID, w.Clock.Now().UTC()); err != nil {
			return published, err
		}
		published++
	}
	return published, errors.Join(failures...)
}

func (w *Worker) handle(ctx context.Context, record OutboxRecord) error {
	switch record.EventType {
	case "workspace.reconcile.requested":
		if record.AggregateType != "workspace" {
			return errors.New("workspace outbox aggregate type is invalid")
		}
		ref, err := w.Actions.Reconcile(ctx, record.AggregateID)
		if err != nil {
			return err
		}
		if ref.State == workspacev1.WorkspaceProvisioning || ref.State == workspacev1.WorkspaceDestroying {
			return ErrOutboxPending
		}
		return nil
	case "workspace.command.dispatch.requested":
		if record.AggregateType != "command" {
			return errors.New("workspace command outbox aggregate type is invalid")
		}
		command, err := w.Store.GetCommandForWorker(ctx, record.AggregateID)
		if err != nil {
			return err
		}
		_, err = w.Actions.Dispatch(ctx, Scope{TenantID: command.TenantID, ProjectID: command.ProjectID, ActorID: command.ActorID}, command.ID)
		return err
	default:
		return errors.New("unknown workspace outbox event type")
	}
}
