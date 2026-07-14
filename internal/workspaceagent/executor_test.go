package workspaceagent

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/keir-research/ai-native-paas/internal/workspace"
	workspacev1 "github.com/keir-research/ai-native-paas/pkg/contracts/workspace/v1"
)

type credentialControlFake struct {
	request workspacev1.AgentCredentialResolve
	view    workspacev1.AgentCredentialView
}

func (f *credentialControlFake) ResolveEnvironment(_ context.Context, request workspacev1.AgentCredentialResolve) (workspacev1.AgentCredentialView, error) {
	f.request = request
	return f.view, nil
}

func TestWorkspaceAgent_RemoteCredentialsAreExactAndBecomeRedactionSentinels(t *testing.T) {
	now := time.Date(2026, 7, 14, 18, 0, 0, 0, time.UTC)
	control := &credentialControlFake{view: workspacev1.AgentCredentialView{
		Values: map[string]string{"GITLAB_TOKEN": "credential-sentinel-never-log"}, ExpiresAt: now.Add(10 * time.Minute),
	}}
	resolver := RemoteEnvironmentResolver{Control: control, Now: func() time.Time { return now }}
	resolved, err := resolver.Resolve(context.Background(), EnvironmentResolutionRequest{
		SessionID: "session-2", ExecutionSessionID: "session-1", CommandID: "command-1",
		References: map[string]string{"GITLAB_TOKEN": "credential://gitlab-project-1"}, CredentialLeases: []string{"gitlab-project-1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if control.request.SessionID != "session-2" || control.request.ExecutionSessionID != "session-1" || control.request.CommandID != "command-1" || resolved.Values["GITLAB_TOKEN"] != "credential-sentinel-never-log" || len(resolved.RedactionValues) != 1 {
		t.Fatalf("request=%#v resolved=%#v", control.request, resolved)
	}
}

func TestWorkspaceAgentRedactor_RedactsSecretAcrossWritesAndANSISequences(t *testing.T) {
	stdout, _ := newBoundedSinks(4096)
	writer := newRedactingWriter(stdout, []string{"secret-sentinel"})
	for _, chunk := range [][]byte{[]byte("before sec"), []byte("ret\x1b[31m-"), []byte("senti"), []byte("nel\x1b[0m after")} {
		if _, err := writer.Write(chunk); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	public := string(stdout.Bytes())
	if strings.Contains(public, "secret-sentinel") || !strings.Contains(public, "[REDACTED]") || strings.Contains(public, "\x1b") {
		t.Fatalf("unsafe redacted output: %q", public)
	}
}

func TestWorkspaceAgentExecutor_RedactsEnvironmentAndUsesBoundedOutput(t *testing.T) {
	root := t.TempDir()
	executor := Executor{WorkspaceRoot: root, Policy: workspace.DefaultCommandPolicy(), Now: time.Now}
	secret := "secret-sentinel"
	code := `import os,sys; v=os.environ["PROJECT_TOKEN"]; sys.stdout.write(v[:6]); sys.stdout.write("\x1b[31m"+v[6:]+"\x1b[0m"); sys.stderr.write("x"*5000)`
	result, err := executor.Execute(context.Background(), "command-1", workspacev1.CommandSpec{
		Argv: []string{"python3", "-c", code}, WorkingDir: "", TimeoutSeconds: 10, OutputLimitBytes: 2048,
	}, ResolvedEnvironment{Values: map[string]string{"PROJECT_TOKEN": secret, "LD_PRELOAD": secret}, RedactionValues: []string{secret}})
	if err != nil || result.State != workspacev1.CommandSucceeded || !result.OutputTruncated {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	public := append(append([]byte(nil), result.Stdout...), result.Stderr...)
	if bytes.Contains(public, []byte(secret)) || len(public) > 2048 {
		t.Fatalf("unsafe or unbounded output length=%d body=%q", len(public), public)
	}
}

func TestWorkspaceAgentExecutor_TimeoutTerminatesForkedProcessGroup(t *testing.T) {
	root := t.TempDir()
	pidFile := filepath.Join(root, "child.pid")
	code := `import os,time; child=os.fork(); (open(` + strconv.Quote(pidFile) + `,"w").write(str(os.getpid())), time.sleep(60)) if child == 0 else time.sleep(60)`
	executor := Executor{WorkspaceRoot: root, Policy: workspace.DefaultCommandPolicy(), Now: time.Now, KillGrace: 100 * time.Millisecond}
	result, err := executor.Execute(context.Background(), "command-1", workspacev1.CommandSpec{
		Argv: []string{"python3", "-c", code}, WorkingDir: "", TimeoutSeconds: 1, OutputLimitBytes: 4096,
	}, ResolvedEnvironment{Values: map[string]string{}})
	if err != nil || result.State != workspacev1.CommandTimedOut || !result.ProcessTreeTerminated {
		t.Fatalf("timeout result=%#v err=%v", result, err)
	}
	raw, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		err = syscall.Kill(pid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("forked child process %d survived timeout: %v", pid, err)
}
