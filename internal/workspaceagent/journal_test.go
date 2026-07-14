package workspaceagent

import (
	"testing"
	"time"

	workspacev1 "github.com/keir-research/ai-native-paas/pkg/contracts/workspace/v1"
)

func journalMessage() workspacev1.AgentMessage {
	return workspacev1.AgentMessage{
		MessageID: "message-1", Kind: workspacev1.AgentMessageExec, CommandID: "command-1", WorkspaceID: "workspace-1", DeliveryAttempt: 1,
		Spec: &workspacev1.CommandSpec{Argv: []string{"tofu", "apply"}, WorkingDir: "repo", TimeoutSeconds: 60, OutputLimitBytes: 4096},
	}
}

func TestWorkspaceAgentJournal_PersistsTerminalOutcomeBeforeReportingAndRejectsChangedRedelivery(t *testing.T) {
	now := time.Date(2026, 7, 14, 16, 0, 0, 0, time.UTC)
	directory := t.TempDir()
	journal, err := NewJournal(directory, "workspace-1", func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	message := journalMessage()
	if _, created, err := journal.Prepare(message); err != nil || !created {
		t.Fatalf("prepare created=%v err=%v", created, err)
	}
	if _, err := journal.MarkRunning("command-1", "session-1"); err != nil {
		t.Fatal(err)
	}
	exit := 0
	outcome := workspacev1.AgentCommandOutcome{CommandID: "command-1", ExecutionSessionID: "session-1", State: workspacev1.CommandSucceeded, ExitCode: &exit, FinishedAt: now}
	if _, err := journal.Finish("command-1", outcome); err != nil {
		t.Fatal(err)
	}

	restarted, err := NewJournal(directory, "workspace-1", func() time.Time { return now.Add(time.Minute) })
	if err != nil {
		t.Fatal(err)
	}
	pending, err := restarted.PendingOutcomes()
	if err != nil || len(pending) != 1 || pending[0].Outcome == nil || pending[0].Outcome.ExecutionSessionID != "session-1" {
		t.Fatalf("pending=%#v err=%v", pending, err)
	}
	message.DeliveryAttempt++
	if record, created, err := restarted.Prepare(message); err != nil || created || record.State != JournalTerminal {
		t.Fatalf("redelivery record=%#v created=%v err=%v", record, created, err)
	}
	changed := message
	changed.Spec = &workspacev1.CommandSpec{Argv: []string{"tofu", "destroy"}, WorkingDir: "repo", TimeoutSeconds: 60, OutputLimitBytes: 4096}
	if _, _, err := restarted.Prepare(changed); err == nil {
		t.Fatal("changed command payload accepted after durable execution")
	}
	if err := restarted.MarkReported("command-1"); err != nil {
		t.Fatal(err)
	}
	if pending, err := restarted.PendingOutcomes(); err != nil || len(pending) != 0 {
		t.Fatalf("reported outcome remains pending=%#v err=%v", pending, err)
	}
}

func TestWorkspaceAgentJournal_RestartNeverBlindlyReexecutesInterruptedApply(t *testing.T) {
	now := time.Date(2026, 7, 14, 16, 30, 0, 0, time.UTC)
	journal, err := NewJournal(t.TempDir(), "workspace-1", func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := journal.Prepare(journalMessage()); err != nil {
		t.Fatal(err)
	}
	if _, err := journal.MarkRunning("command-1", "session-1"); err != nil {
		t.Fatal(err)
	}
	if count, err := journal.RecoverInterrupted(); err != nil || count != 1 {
		t.Fatalf("recovered=%d err=%v", count, err)
	}
	pending, err := journal.PendingOutcomes()
	if err != nil || len(pending) != 1 || pending[0].Outcome.State != workspacev1.CommandFailed || !pending[0].Outcome.ProcessTreeTerminated || pending[0].Outcome.ExecutionSessionID != "session-1" {
		t.Fatalf("recovered outcome=%#v err=%v", pending, err)
	}
}
