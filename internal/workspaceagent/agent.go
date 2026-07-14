package workspaceagent

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/keir-research/ai-native-paas/internal/workspace"
	workspacev1 "github.com/keir-research/ai-native-paas/pkg/contracts/workspace/v1"
)

type ControlPlane interface {
	Connect(context.Context) (workspacev1.AgentSessionView, error)
	Heartbeat(context.Context, string) error
	Next(context.Context, string) (*workspacev1.AgentMessage, error)
	Acknowledge(context.Context, string, string, bool) error
	Outcome(context.Context, workspacev1.AgentCommandOutcome) error
	ResolveEnvironment(context.Context, workspacev1.AgentCredentialResolve) (workspacev1.AgentCredentialView, error)
	UploadOutput(context.Context, workspacev1.AgentOutputChunk) error
	Rotate(context.Context, string) error
	CertificateNotAfter() time.Time
}

type CommandExecutor interface {
	Execute(context.Context, string, workspacev1.CommandSpec, ResolvedEnvironment) (ExecutionResult, error)
}

type OutputSink interface {
	Persist(commandID string, stdout, stderr []byte, truncated bool) error
	Stream(commandID string, emit func(workspacev1.AgentOutputChunk) error) error
}

type Agent struct {
	WorkspaceID       string
	Control           ControlPlane
	Journal           *Journal
	Resolver          EnvironmentResolver
	Executor          CommandExecutor
	Outputs           OutputSink
	Policy            workspace.CommandPolicy
	Now               func() time.Time
	Log               func(string, ...any)
	SystemEnvironment map[string]string
	active            *activeCommand
	results           chan commandCompletion
}

type activeCommand struct {
	commandID          string
	executionSessionID string
	cancel             context.CancelFunc
}

type commandCompletion struct {
	commandID          string
	executionSessionID string
	result             ExecutionResult
	environment        ResolvedEnvironment
}

var errSessionRotated = errors.New("workspace agent certificate rotated")

func (a *Agent) Run(ctx context.Context) error {
	if err := a.require(); err != nil {
		return err
	}
	if _, err := a.Journal.RecoverInterrupted(); err != nil {
		return err
	}
	a.results = make(chan commandCompletion, 1)
	backoff := time.Second
	for ctx.Err() == nil {
		session, err := a.Control.Connect(ctx)
		if err != nil {
			a.log("workspace agent connect retry")
			if !waitContext(ctx, backoff) {
				break
			}
			backoff = nextBackoff(backoff)
			continue
		}
		backoff = time.Second
		err = a.runSession(ctx, session)
		if ctx.Err() != nil {
			break
		}
		if err != nil && !errors.Is(err, errSessionRotated) {
			a.log("workspace agent session reconnect")
			if !waitContext(ctx, backoff) {
				break
			}
			backoff = nextBackoff(backoff)
		}
	}
	if a.active != nil {
		a.active.cancel()
	}
	return ctx.Err()
}

func (a *Agent) runSession(ctx context.Context, session workspacev1.AgentSessionView) error {
	if strings.TrimSpace(session.SessionID) == "" || session.WorkspaceID != a.WorkspaceID {
		return errors.New("workspace agent session identity mismatch")
	}
	lastHeartbeat := time.Time{}
	for ctx.Err() == nil {
		if err := a.consumeResult(session.SessionID); err != nil {
			return err
		}
		if err := a.flushOutcomes(ctx, session.SessionID); err != nil {
			return err
		}
		now := a.Now().UTC()
		if !a.Control.CertificateNotAfter().After(now.Add(4 * time.Minute)) {
			if err := a.Control.Rotate(ctx, session.SessionID); err != nil {
				return err
			}
			return errSessionRotated
		}
		if lastHeartbeat.IsZero() || !now.Before(lastHeartbeat.Add(20*time.Second)) {
			if err := a.Control.Heartbeat(ctx, session.SessionID); err != nil {
				return err
			}
			lastHeartbeat = now
		}
		message, err := a.Control.Next(ctx, session.SessionID)
		if err != nil {
			return err
		}
		if message == nil {
			continue
		}
		switch message.Kind {
		case workspacev1.AgentMessageExec:
			if err := a.handleExec(ctx, session.SessionID, *message); err != nil {
				return err
			}
		case workspacev1.AgentMessageCancel:
			if err := a.handleCancel(ctx, session.SessionID, *message); err != nil {
				return err
			}
		default:
			if err := a.Control.Acknowledge(ctx, session.SessionID, message.MessageID, false); err != nil {
				return err
			}
		}
	}
	return ctx.Err()
}

func (a *Agent) handleExec(ctx context.Context, sessionID string, message workspacev1.AgentMessage) error {
	if message.WorkspaceID != a.WorkspaceID || message.Spec == nil || a.Policy.Validate(*message.Spec) != nil {
		return a.Control.Acknowledge(ctx, sessionID, message.MessageID, false)
	}
	record, _, err := a.Journal.Prepare(message)
	if err != nil {
		_ = a.Control.Acknowledge(ctx, sessionID, message.MessageID, false)
		return err
	}
	if record.State == JournalTerminal {
		if err := a.Control.Acknowledge(ctx, sessionID, message.MessageID, true); err != nil {
			return err
		}
		return a.flushOutcomes(ctx, sessionID)
	}
	if a.active != nil {
		accepted := a.active.commandID == message.CommandID
		return a.Control.Acknowledge(ctx, sessionID, message.MessageID, accepted)
	}
	if record.State == JournalRunning {
		_, err := a.Journal.Finish(message.CommandID, workspacev1.AgentCommandOutcome{
			CommandID: message.CommandID, ExecutionSessionID: sessionID, State: workspacev1.CommandFailed,
			FinishedAt: a.Now().UTC(), ProcessTreeTerminated: true,
		})
		if err != nil {
			return err
		}
		if err := a.Control.Acknowledge(ctx, sessionID, message.MessageID, true); err != nil {
			return err
		}
		return a.flushOutcomes(ctx, sessionID)
	}
	environment, resolveErr := a.Resolver.Resolve(ctx, EnvironmentResolutionRequest{
		SessionID: sessionID, ExecutionSessionID: sessionID, CommandID: message.CommandID,
		References: message.Spec.EnvironmentRefs, CredentialLeases: message.CredentialLeases,
	})
	if _, err := a.Journal.MarkRunning(message.CommandID, sessionID); err != nil {
		return err
	}
	if err := a.Control.Acknowledge(ctx, sessionID, message.MessageID, true); err != nil {
		_, _ = a.Journal.Finish(message.CommandID, workspacev1.AgentCommandOutcome{
			CommandID: message.CommandID, ExecutionSessionID: sessionID, State: workspacev1.CommandFailed,
			FinishedAt: a.Now().UTC(), ProcessTreeTerminated: true,
		})
		clearEnvironment(&environment)
		return err
	}
	if resolveErr != nil {
		_, err := a.Journal.Finish(message.CommandID, workspacev1.AgentCommandOutcome{
			CommandID: message.CommandID, ExecutionSessionID: sessionID, State: workspacev1.CommandFailed, FinishedAt: a.Now().UTC(),
		})
		clearEnvironment(&environment)
		if err != nil {
			return err
		}
		return a.flushOutcomes(ctx, sessionID)
	}
	environment.SystemValues = cloneSystemEnvironment(a.SystemEnvironment)
	environment.SystemValues["PLATFORM_COMMAND_ID"] = message.CommandID
	environment.SystemValues["PLATFORM_EXECUTION_SESSION_ID"] = sessionID
	executionContext, cancel := context.WithCancel(ctx)
	a.active = &activeCommand{commandID: message.CommandID, executionSessionID: sessionID, cancel: cancel}
	spec := cloneCommandSpec(*message.Spec)
	go func() {
		result, executeErr := a.Executor.Execute(executionContext, message.CommandID, spec, environment)
		if executeErr != nil {
			result = ExecutionResult{State: workspacev1.CommandFailed, FinishedAt: a.Now().UTC()}
		}
		a.results <- commandCompletion{commandID: message.CommandID, executionSessionID: sessionID, result: result, environment: environment}
	}()
	return nil
}

func (a *Agent) handleCancel(ctx context.Context, sessionID string, message workspacev1.AgentMessage) error {
	if message.WorkspaceID != a.WorkspaceID || !identityPattern.MatchString(message.CommandID) {
		return a.Control.Acknowledge(ctx, sessionID, message.MessageID, false)
	}
	if err := a.Control.Acknowledge(ctx, sessionID, message.MessageID, true); err != nil {
		return err
	}
	if a.active != nil && a.active.commandID == message.CommandID {
		a.active.cancel()
	}
	return nil
}

func (a *Agent) consumeResult(sessionID string) error {
	select {
	case completion := <-a.results:
		if a.active == nil || a.active.commandID != completion.commandID || a.active.executionSessionID != completion.executionSessionID {
			clearEnvironment(&completion.environment)
			return errors.New("workspace command completion identity mismatch")
		}
		a.active = nil
		if err := a.Outputs.Persist(completion.commandID, completion.result.Stdout, completion.result.Stderr, completion.result.OutputTruncated); err != nil {
			a.log("workspace command output persistence failed")
			completion.result.State = workspacev1.CommandFailed
			completion.result.ExitCode = nil
		} else if err := a.Journal.MarkOutputReady(completion.commandID); err != nil {
			return err
		}
		outcome := workspacev1.AgentCommandOutcome{
			CommandID: completion.commandID, ExecutionSessionID: completion.executionSessionID, State: completion.result.State,
			ExitCode: completion.result.ExitCode, FinishedAt: completion.result.FinishedAt,
			ProcessTreeTerminated: completion.result.ProcessTreeTerminated,
		}
		_, err := a.Journal.Finish(completion.commandID, outcome)
		clearEnvironment(&completion.environment)
		return err
	default:
		return nil
	}
}

func (a *Agent) flushOutcomes(ctx context.Context, sessionID string) error {
	records, err := a.Journal.PendingOutcomes()
	if err != nil {
		return err
	}
	for _, record := range records {
		if !record.OutputReady {
			if err := a.Outputs.Persist(record.CommandID, nil, nil, false); err != nil {
				return err
			}
			if err := a.Journal.MarkOutputReady(record.CommandID); err != nil {
				return err
			}
		}
		if !record.OutputUploaded {
			if err := a.Outputs.Stream(record.CommandID, func(chunk workspacev1.AgentOutputChunk) error {
				chunk.SessionID = sessionID
				chunk.ExecutionSessionID = record.ExecutionSessionID
				return a.Control.UploadOutput(ctx, chunk)
			}); err != nil {
				return err
			}
			if err := a.Journal.MarkOutputUploaded(record.CommandID); err != nil {
				return err
			}
		}
		outcome := *record.Outcome
		outcome.SessionID = sessionID
		if strings.TrimSpace(outcome.ExecutionSessionID) == "" {
			outcome.ExecutionSessionID = sessionID
		}
		if err := a.Control.Outcome(ctx, outcome); err != nil {
			return err
		}
		if err := a.Journal.MarkReported(record.CommandID); err != nil {
			return err
		}
	}
	return nil
}

func (a *Agent) require() error {
	if !identityPattern.MatchString(a.WorkspaceID) || a.Control == nil || a.Journal == nil || a.Resolver == nil || a.Executor == nil || a.Outputs == nil || a.Now == nil || len(a.Policy.AllowedExecutables) == 0 {
		return errors.New("workspace agent dependencies are unavailable")
	}
	return nil
}

func cloneSystemEnvironment(values map[string]string) map[string]string {
	result := make(map[string]string, len(values)+2)
	for name, value := range values {
		if systemEnvironmentName(name) && value != "" && !strings.ContainsRune(value, '\x00') {
			result[name] = value
		}
	}
	return result
}

func (a *Agent) log(message string, values ...any) {
	if a.Log != nil {
		a.Log(message, values...)
	}
}

func clearEnvironment(environment *ResolvedEnvironment) {
	if environment == nil {
		return
	}
	for name := range environment.Values {
		environment.Values[name] = ""
		delete(environment.Values, name)
	}
	for name := range environment.SystemValues {
		environment.SystemValues[name] = ""
		delete(environment.SystemValues, name)
	}
	for index := range environment.RedactionValues {
		environment.RedactionValues[index] = ""
	}
	environment.RedactionValues = nil
}

func waitContext(ctx context.Context, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func nextBackoff(value time.Duration) time.Duration {
	value *= 2
	if value > 30*time.Second {
		return 30 * time.Second
	}
	return value
}
