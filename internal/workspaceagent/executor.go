package workspaceagent

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/keir-research/ai-native-paas/internal/workspace"
	workspacev1 "github.com/keir-research/ai-native-paas/pkg/contracts/workspace/v1"
)

type ResolvedEnvironment struct {
	Values          map[string]string
	RedactionValues []string
}

type EnvironmentResolver interface {
	Resolve(context.Context, map[string]string, []string) (ResolvedEnvironment, error)
}

type FailClosedEnvironmentResolver struct{}

func (FailClosedEnvironmentResolver) Resolve(_ context.Context, references map[string]string, leases []string) (ResolvedEnvironment, error) {
	if len(references) != 0 || len(leases) != 0 {
		return ResolvedEnvironment{}, errors.New("workspace credential gateway is unavailable")
	}
	return ResolvedEnvironment{Values: map[string]string{}}, nil
}

type ExecutionResult struct {
	State                 workspacev1.CommandState
	ExitCode              *int
	FinishedAt            time.Time
	ProcessTreeTerminated bool
	Stdout                []byte
	Stderr                []byte
	OutputTruncated       bool
}

type Executor struct {
	WorkspaceRoot string
	Policy        workspace.CommandPolicy
	Now           func() time.Time
	KillGrace     time.Duration
	RequireCgroup bool
}

func (e Executor) Execute(parent context.Context, commandID string, spec workspacev1.CommandSpec, environment ResolvedEnvironment) (ExecutionResult, error) {
	if e.Now == nil || !identityPattern.MatchString(commandID) || !filepath.IsAbs(e.WorkspaceRoot) || filepath.Clean(e.WorkspaceRoot) != e.WorkspaceRoot || e.WorkspaceRoot == "/" || e.Policy.Validate(spec) != nil {
		return ExecutionResult{}, errors.New("workspace command execution policy denied")
	}
	workingDirectory := filepath.Join(e.WorkspaceRoot, filepath.Clean(spec.WorkingDir))
	relative, err := filepath.Rel(e.WorkspaceRoot, workingDirectory)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return ExecutionResult{}, errors.New("workspace command directory escapes workspace root")
	}
	timeout := time.Duration(spec.TimeoutSeconds) * time.Second
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	command := exec.Command(spec.Argv[0], spec.Argv[1:]...)
	command.Dir = workingDirectory
	command.Env = commandEnvironment(environment.Values)
	control, err := newProcessControl(commandID, e.RequireCgroup)
	if err != nil {
		return ExecutionResult{}, err
	}
	defer control.Close()
	control.Configure(command)
	stdoutSink, stderrSink := newBoundedSinks(spec.OutputLimitBytes)
	stdout := newRedactingWriter(stdoutSink, environment.RedactionValues)
	stderr := newRedactingWriter(stderrSink, environment.RedactionValues)
	command.Stdout = stdout
	command.Stderr = stderr
	if err := command.Start(); err != nil {
		_ = stdout.Close()
		_ = stderr.Close()
		return ExecutionResult{}, errors.New("start workspace command")
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	result := ExecutionResult{}
	select {
	case waitErr := <-done:
		result.State, result.ExitCode = classifyExit(waitErr)
	case <-ctx.Done():
		result.ProcessTreeTerminated = true
		control.Terminate(command.Process.Pid)
		grace := e.KillGrace
		if grace <= 0 || grace > 5*time.Second {
			grace = 500 * time.Millisecond
		}
		timer := time.NewTimer(grace)
		select {
		case <-done:
			if !timer.Stop() {
				<-timer.C
			}
		case <-timer.C:
			control.Kill(command.Process.Pid)
			<-done
		}
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			result.State = workspacev1.CommandTimedOut
		} else {
			result.State = workspacev1.CommandCanceled
		}
	}
	_ = stdout.Close()
	_ = stderr.Close()
	result.FinishedAt = e.Now().UTC()
	result.Stdout = stdoutSink.Bytes()
	result.Stderr = stderrSink.Bytes()
	result.OutputTruncated = stdoutSink.Truncated() || stderrSink.Truncated()
	return result, nil
}

func classifyExit(err error) (workspacev1.CommandState, *int) {
	if err == nil {
		exit := 0
		return workspacev1.CommandSucceeded, &exit
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		exit := exitError.ExitCode()
		return workspacev1.CommandFailed, &exit
	}
	return workspacev1.CommandFailed, nil
}

func commandEnvironment(values map[string]string) []string {
	result := []string{
		"HOME=/home/workspace-agent", "LANG=C.UTF-8", "LC_ALL=C.UTF-8",
		"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", "TMPDIR=/var/tmp",
	}
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sortStrings(names)
	for _, name := range names {
		if environmentVariableName(name) && !reservedEnvironmentName(name) && !strings.ContainsRune(values[name], '\x00') {
			result = append(result, name+"="+values[name])
		}
	}
	return result
}

func environmentVariableName(value string) bool {
	if value == "" || !(value[0] == '_' || value[0] >= 'A' && value[0] <= 'Z') {
		return false
	}
	for _, character := range value[1:] {
		if character != '_' && (character < 'A' || character > 'Z') && (character < '0' || character > '9') {
			return false
		}
	}
	return true
}

func reservedEnvironmentName(value string) bool {
	switch value {
	case "HOME", "PATH", "SHELL", "LD_PRELOAD", "LD_LIBRARY_PATH", "GODEBUG", "GOTRACEBACK", "TMPDIR":
		return true
	default:
		return strings.HasPrefix(value, "SYSTEMD_")
	}
}

func sortStrings(values []string) {
	for index := 1; index < len(values); index++ {
		for cursor := index; cursor > 0 && values[cursor] < values[cursor-1]; cursor-- {
			values[cursor], values[cursor-1] = values[cursor-1], values[cursor]
		}
	}
}
