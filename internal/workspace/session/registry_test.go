package session

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/keir-research/ai-native-paas/internal/workspace"
	workspacev1 "github.com/keir-research/ai-native-paas/pkg/contracts/workspace/v1"
)

type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}
func (c *testClock) Add(value time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(value)
}

type testIDs struct {
	mu   sync.Mutex
	next int
}

func (i *testIDs) New(prefix string) string {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.next++
	return fmt.Sprintf("%s-%d", prefix, i.next)
}

func newRegistryFixture() (*Registry, *MemoryStore, *testClock, Principal, workspacev1.AgentSessionView) {
	clock := &testClock{now: time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)}
	store := NewMemoryStore()
	registry := &Registry{Store: store, Clock: clock, IDs: &testIDs{}, AckPollInterval: time.Millisecond, DispatchAckTimeout: time.Second}
	principal := Principal{TenantID: "tenant-1", ProjectID: "project-1", WorkspaceID: "workspace-1", TaskID: "task-1", AgentID: "agent-1", CertificateID: "certificate-1", NotAfter: clock.Now().Add(10 * time.Minute)}
	view, err := registry.Connect(context.Background(), principal, "vm-1")
	if err != nil {
		panic(err)
	}
	return registry, store, clock, principal, view
}

func commandEnvelope() workspace.CommandEnvelope {
	return workspace.CommandEnvelope{
		CommandID: "command-1", WorkspaceID: "workspace-1", ProjectID: "project-1", TaskID: "task-1",
		Spec:             workspacev1.CommandSpec{Argv: []string{"tofu", "plan"}, WorkingDir: "repo/infrastructure/tofu", EnvironmentRefs: map[string]string{"STATE_TOKEN": "state://project-1"}, TimeoutSeconds: 60, OutputLimitBytes: 1 << 20},
		CredentialLeases: []string{"credential/project-1/git"},
	}
}

func TestSession_OutboundAgentAcknowledgementSurvivesRegistryRestart(t *testing.T) {
	registry, store, clock, principal, sessionView := newRegistryFixture()
	type result struct {
		receipt workspace.DispatchReceipt
		err     error
	}
	done := make(chan result, 1)
	go func() {
		receipt, err := registry.Dispatch(context.Background(), commandEnvelope())
		done <- result{receipt: receipt, err: err}
	}()

	var message workspacev1.AgentMessage
	var err error
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		message, err = registry.Next(context.Background(), principal, sessionView.SessionID)
		if err == nil {
			break
		}
		if !errors.Is(err, ErrNotFound) {
			t.Fatal(err)
		}
		time.Sleep(time.Millisecond)
	}
	if err != nil {
		t.Fatalf("agent did not receive outbound command: %v", err)
	}
	if message.Kind != workspacev1.AgentMessageExec || message.CommandID != "command-1" || message.Spec == nil || message.Spec.Argv[0] != "tofu" || message.DeliveryAttempt != 1 {
		t.Fatalf("unexpected agent message: %#v", message)
	}
	if err := registry.Acknowledge(context.Background(), principal, sessionView.SessionID, message.MessageID, true); err != nil {
		t.Fatal(err)
	}
	first := <-done
	if first.err != nil || !first.receipt.Accepted || first.receipt.VMID != "vm-1" || first.receipt.AgentSessionID != sessionView.SessionID {
		t.Fatalf("dispatch receipt=%#v err=%v", first.receipt, first.err)
	}

	// A process restart gets the durable ACK and original identity. It cannot
	// enqueue or execute the same stateful command a second time.
	restarted := &Registry{Store: store, Clock: clock, IDs: &testIDs{}, AckPollInterval: time.Millisecond, DispatchAckTimeout: time.Second}
	replayed, err := restarted.Dispatch(context.Background(), commandEnvelope())
	if err != nil || replayed != first.receipt {
		t.Fatalf("restart replay receipt=%#v err=%v", replayed, err)
	}
}

func TestSession_CertificateScopeCannotCrossWorkspaceOrAcknowledgeForeignMessage(t *testing.T) {
	registry, _, clock, principal, sessionView := newRegistryFixture()
	foreign := principal
	foreign.WorkspaceID = "workspace-2"
	foreign.CertificateID = "certificate-2"
	foreign.NotAfter = clock.Now().Add(10 * time.Minute)
	if err := registry.Heartbeat(context.Background(), foreign, sessionView.SessionID); !errors.Is(err, ErrConflict) {
		t.Fatalf("foreign certificate heartbeat err=%v", err)
	}
	if err := registry.Acknowledge(context.Background(), foreign, sessionView.SessionID, "message-unknown", true); !errors.Is(err, ErrConflict) {
		t.Fatalf("foreign certificate acknowledgement err=%v", err)
	}
}

func TestSession_UnacknowledgedDeliveryIsRedeliveredAfterLease(t *testing.T) {
	registry, _, clock, principal, sessionView := newRegistryFixture()
	registry.DeliveryLease = 2 * time.Second
	if _, err := registry.queue(context.Background(), workspacev1.AgentMessage{Kind: workspacev1.AgentMessageCancel, CommandID: "command-1", WorkspaceID: "workspace-1"}); err != nil {
		t.Fatal(err)
	}
	first, err := registry.Next(context.Background(), principal, sessionView.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Next(context.Background(), principal, sessionView.SessionID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("leased delivery was repeated early: %v", err)
	}
	clock.Add(3 * time.Second)
	second, err := registry.Next(context.Background(), principal, sessionView.SessionID)
	if err != nil || second.MessageID != first.MessageID || second.DeliveryAttempt != 2 {
		t.Fatalf("redelivery=%#v err=%v", second, err)
	}
}

func TestSession_DestroyClosesCertificateBoundChannelIdempotently(t *testing.T) {
	registry, _, _, principal, sessionView := newRegistryFixture()
	if err := registry.Close(context.Background(), principal.WorkspaceID); err != nil {
		t.Fatal(err)
	}
	if err := registry.Close(context.Background(), principal.WorkspaceID); err != nil {
		t.Fatal(err)
	}
	if err := registry.Heartbeat(context.Background(), principal, sessionView.SessionID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("closed workspace channel remained usable: %v", err)
	}
}
