package workspace

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	workspacev1 "github.com/keir-research/ai-native-paas/pkg/contracts/workspace/v1"
)

type workerStoreFake struct {
	mu        sync.Mutex
	records   []OutboxRecord
	commands  map[string]Command
	owner     string
	lease     time.Time
	published map[int64]int
}

func (s *workerStoreFake) ClaimOutbox(_ context.Context, owner string, now, until time.Time, _ int) ([]OutboxRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.owner != "" && s.lease.After(now) {
		return []OutboxRecord{}, nil
	}
	s.owner, s.lease = owner, until
	result := make([]OutboxRecord, 0, len(s.records))
	for _, record := range s.records {
		if s.published[record.EventID] == 0 {
			record.DeliveryAttempts++
			result = append(result, record)
		}
	}
	return result, nil
}

func (s *workerStoreFake) DeferOutbox(_ context.Context, eventID int64, owner string, until time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if eventID < 1 || s.owner != owner {
		return ErrConflict
	}
	s.lease = until
	return nil
}

func (s *workerStoreFake) MarkOutboxPublished(_ context.Context, eventID int64, owner string, _ time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.owner != owner || s.published[eventID] != 0 {
		return ErrConflict
	}
	s.published[eventID]++
	s.owner = ""
	s.lease = time.Time{}
	return nil
}

func (s *workerStoreFake) GetCommandForWorker(_ context.Context, id string) (Command, error) {
	command, ok := s.commands[id]
	if !ok {
		return Command{}, ErrNotFound
	}
	return command, nil
}

type workerActionsFake struct {
	mu             sync.Mutex
	reconcileState workspacev1.WorkspaceState
	reconciles     int
	dispatches     int
	dispatchScope  Scope
	effects        map[string]int
}

func (a *workerActionsFake) Reconcile(_ context.Context, id string) (workspacev1.WorkspaceRef, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.reconciles++
	if a.effects == nil {
		a.effects = make(map[string]int)
	}
	if a.effects[id] == 0 {
		a.effects[id]++
	}
	return workspacev1.WorkspaceRef{WorkspaceID: id, State: a.reconcileState}, nil
}

func (a *workerActionsFake) Dispatch(_ context.Context, scope Scope, commandID string) (workspacev1.CommandView, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.dispatches++
	a.dispatchScope = scope
	if a.effects == nil {
		a.effects = make(map[string]int)
	}
	if a.effects[commandID] == 0 {
		a.effects[commandID]++
	}
	return workspacev1.CommandView{CommandID: commandID, State: workspacev1.CommandRunning}, nil
}

func TestWorkspaceWorker_CrashAfterEffectReplaysIdempotently(t *testing.T) {
	clock := &testClock{now: time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)}
	store := &workerStoreFake{
		records:   []OutboxRecord{{EventID: 1, AggregateType: "workspace", AggregateID: "workspace-1", EventType: "workspace.reconcile.requested"}},
		published: make(map[int64]int),
	}
	actions := &workerActionsFake{reconcileState: workspacev1.WorkspaceReady}
	crash := errors.New("simulated process crash")
	worker := &Worker{Store: store, Actions: actions, Clock: clock, WorkerID: "worker-a", Lease: time.Minute, AfterEffect: func(OutboxRecord) error { return crash }}
	if published, err := worker.RunOnce(context.Background(), 10); published != 0 || !errors.Is(err, crash) {
		t.Fatalf("crash result published=%d err=%v", published, err)
	}
	if actions.effects["workspace-1"] != 1 || store.published[1] != 0 {
		t.Fatalf("effect/publish before restart effects=%v published=%v", actions.effects, store.published)
	}
	clock.Add(time.Minute + time.Second)
	worker.WorkerID = "worker-b"
	worker.AfterEffect = nil
	if published, err := worker.RunOnce(context.Background(), 10); err != nil || published != 1 {
		t.Fatalf("restart result published=%d err=%v", published, err)
	}
	if actions.reconciles != 2 || actions.effects["workspace-1"] != 1 || store.published[1] != 1 {
		t.Fatalf("replay was not idempotent reconciles=%d effects=%v published=%v", actions.reconciles, actions.effects, store.published)
	}
}

func TestWorkspaceWorker_PendingReadinessIsDeferredNotPublished(t *testing.T) {
	clock := &testClock{now: time.Date(2026, 7, 14, 12, 15, 0, 0, time.UTC)}
	store := &workerStoreFake{
		records:   []OutboxRecord{{EventID: 2, AggregateType: "workspace", AggregateID: "workspace-2", EventType: "workspace.reconcile.requested"}},
		published: make(map[int64]int),
	}
	actions := &workerActionsFake{reconcileState: workspacev1.WorkspaceProvisioning}
	worker := &Worker{Store: store, Actions: actions, Clock: clock, WorkerID: "worker-a", Lease: time.Minute, RetryDelay: 3 * time.Second}
	if published, err := worker.RunOnce(context.Background(), 10); err != nil || published != 0 {
		t.Fatalf("pending result published=%d err=%v", published, err)
	}
	if store.published[2] != 0 || !store.lease.Equal(clock.Now().Add(3*time.Second)) {
		t.Fatalf("pending event was acknowledged or not deferred: published=%v lease=%v", store.published, store.lease)
	}
}

func TestWorkspaceWorker_CommandScopeComesFromDurableCommand(t *testing.T) {
	clock := &testClock{now: time.Date(2026, 7, 14, 12, 30, 0, 0, time.UTC)}
	store := &workerStoreFake{
		records: []OutboxRecord{{EventID: 3, AggregateType: "command", AggregateID: "command-1", EventType: "workspace.command.dispatch.requested"}},
		commands: map[string]Command{
			"command-1": {ID: "command-1", TenantID: "tenant-stored", ProjectID: "project-stored", ActorID: "agent-stored"},
		},
		published: make(map[int64]int),
	}
	actions := &workerActionsFake{}
	worker := &Worker{Store: store, Actions: actions, Clock: clock, WorkerID: "worker-a"}
	if published, err := worker.RunOnce(context.Background(), 10); err != nil || published != 1 {
		t.Fatalf("dispatch result published=%d err=%v", published, err)
	}
	want := Scope{TenantID: "tenant-stored", ProjectID: "project-stored", ActorID: "agent-stored"}
	if actions.dispatchScope != want {
		t.Fatalf("dispatch scope=%#v want=%#v", actions.dispatchScope, want)
	}
}
