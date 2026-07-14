package workspaceagent

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/keir-research/ai-native-paas/internal/workspace"
	workspacev1 "github.com/keir-research/ai-native-paas/pkg/contracts/workspace/v1"
)

type agentControlFake struct {
	mu              sync.Mutex
	outcomeFailures int
	outcomes        []workspacev1.AgentCommandOutcome
	acks            []bool
}

func (c *agentControlFake) Connect(context.Context) (workspacev1.AgentSessionView, error) {
	return workspacev1.AgentSessionView{}, errors.New("unused")
}
func (c *agentControlFake) Heartbeat(context.Context, string) error { return nil }
func (c *agentControlFake) Next(context.Context, string) (*workspacev1.AgentMessage, error) {
	return nil, nil
}
func (c *agentControlFake) Acknowledge(_ context.Context, _, _ string, accepted bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.acks = append(c.acks, accepted)
	return nil
}
func (c *agentControlFake) Outcome(_ context.Context, outcome workspacev1.AgentCommandOutcome) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.outcomeFailures > 0 {
		c.outcomeFailures--
		return errors.New("lost response")
	}
	c.outcomes = append(c.outcomes, outcome)
	return nil
}
func (c *agentControlFake) ResolveEnvironment(context.Context, workspacev1.AgentCredentialResolve) (workspacev1.AgentCredentialView, error) {
	return workspacev1.AgentCredentialView{}, errors.New("unused")
}
func (c *agentControlFake) Rotate(context.Context, string) error { return nil }
func (c *agentControlFake) CertificateNotAfter() time.Time       { return time.Now().Add(time.Hour) }

type executorFake struct{ calls int }

func (e *executorFake) Execute(context.Context, string, workspacev1.CommandSpec, ResolvedEnvironment) (ExecutionResult, error) {
	e.calls++
	exit := 0
	return ExecutionResult{State: workspacev1.CommandSucceeded, ExitCode: &exit, FinishedAt: time.Date(2026, 7, 14, 17, 0, 0, 0, time.UTC), Stdout: []byte("safe")}, nil
}

type outputSinkFake struct{ calls int }

func (s *outputSinkFake) Persist(string, []byte, []byte, bool) error { s.calls++; return nil }

func TestWorkspaceAgent_LostOutcomeResponseIsReplayedWithoutRepeatingCommand(t *testing.T) {
	now := time.Date(2026, 7, 14, 17, 0, 0, 0, time.UTC)
	journal, err := NewJournal(t.TempDir(), "workspace-1", func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	control := &agentControlFake{outcomeFailures: 1}
	executor := &executorFake{}
	outputs := &outputSinkFake{}
	agent := &Agent{
		WorkspaceID: "workspace-1", Control: control, Journal: journal, Resolver: FailClosedEnvironmentResolver{}, Executor: executor,
		Outputs: outputs, Policy: workspace.DefaultCommandPolicy(), Now: func() time.Time { return now }, results: make(chan commandCompletion, 1),
	}
	message := journalMessage()
	if err := agent.handleExec(context.Background(), "session-1", message); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if err := agent.consumeResult("session-1"); err != nil {
			t.Fatal(err)
		}
		pending, _ := journal.PendingOutcomes()
		if len(pending) == 1 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if err := agent.flushOutcomes(context.Background(), "session-1"); err == nil {
		t.Fatal("simulated lost outcome response did not fail")
	}

	restarted := &Agent{
		WorkspaceID: "workspace-1", Control: control, Journal: journal, Resolver: FailClosedEnvironmentResolver{}, Executor: executor,
		Outputs: outputs, Policy: workspace.DefaultCommandPolicy(), Now: func() time.Time { return now.Add(time.Minute) }, results: make(chan commandCompletion, 1),
	}
	message.DeliveryAttempt++
	if err := restarted.handleExec(context.Background(), "session-2", message); err != nil {
		t.Fatal(err)
	}
	if executor.calls != 1 || outputs.calls != 1 || len(control.outcomes) != 1 {
		t.Fatalf("calls executor=%d outputs=%d outcomes=%d", executor.calls, outputs.calls, len(control.outcomes))
	}
	reported := control.outcomes[0]
	if reported.SessionID != "session-2" || reported.ExecutionSessionID != "session-1" || reported.State != workspacev1.CommandSucceeded {
		t.Fatalf("reported outcome=%#v", reported)
	}
}
