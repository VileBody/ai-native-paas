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

func TestWorkspaceAgentExecutor_SeparatesTaskUIDFromIdentityReader(t *testing.T) {
	config := processIdentityConfig{
		Required: true, TaskUID: 1001, TaskGID: 1001, VerifiedUID: 1002, VerifiedGID: 1002,
		IdentityConfig: Config{
			ControlPlaneURL: "https://workspace.example.com", EgressGatewayURL: "https://egress.example.com:8443",
			WorkspaceID: "workspace-1", CorrelationID: "correlation-1", CertificateFile: "/identity/agent.crt",
			PrivateKeyFile: "/identity/agent.key", CAFile: "/identity/ca.crt", JournalDirectory: "/state/journal", WorkspaceRoot: "/workspace",
		},
	}
	task, err := selectProcessIdentity(workspacev1.CommandSpec{Argv: []string{"python3", "task.py"}}, config)
	if err != nil || task.UID != 1001 || task.GID != 1001 || task.Home != "/home/workspace-task" || task.AttachIdentity {
		t.Fatalf("task identity=%#v err=%v", task, err)
	}
	verified, err := selectProcessIdentity(workspacev1.CommandSpec{Argv: []string{"workspace-agent", "verified-git-commit"}}, config)
	if err != nil || verified.UID != 1002 || verified.GID != 1002 || verified.Home != "/home/workspace-verified" || !verified.AttachIdentity || !verified.AttachCredentials {
		t.Fatalf("verified identity=%#v err=%v", verified, err)
	}
	patch, err := selectProcessIdentity(workspacev1.CommandSpec{Argv: []string{"workspace-agent", "verified-git-apply-patch"}}, config)
	if err != nil || !patch.AttachIdentity || patch.AttachCredentials {
		t.Fatalf("patch identity=%#v err=%v", patch, err)
	}
	if _, err := selectProcessIdentity(workspacev1.CommandSpec{Argv: []string{"workspace-agent", "unknown"}}, config); err == nil {
		t.Fatal("unknown workspace-agent subcommand received identity-reader UID")
	}
	if _, err := selectProcessIdentity(workspacev1.CommandSpec{Argv: []string{"python3"}}, processIdentityConfig{Required: true}); err == nil {
		t.Fatal("zero task identity accepted")
	}
	shared := config
	shared.VerifiedUID, shared.VerifiedGID = shared.TaskUID, shared.TaskGID
	if _, err := selectProcessIdentity(workspacev1.CommandSpec{Argv: []string{"workspace-agent", "verified-tofu-plan"}}, shared); err == nil {
		t.Fatal("task and verified operations shared one UID")
	}
}

func TestWorkspaceAgentExecutor_TimeoutTerminatesForkedProcessGroup(t *testing.T) {
	root := t.TempDir()
	pidFile := filepath.Join(root, "process-group.pid")
	code := `import os,time; f=open(` + strconv.Quote(pidFile) + `,"w"); f.write(str(os.getpid())); f.flush(); os.fsync(f.fileno()); f.close(); os.fork(); time.sleep(60)`
	executor := Executor{WorkspaceRoot: root, Policy: workspace.DefaultCommandPolicy(), Now: time.Now, KillGrace: 100 * time.Millisecond}
	result, err := executor.Execute(context.Background(), "command-1", workspacev1.CommandSpec{
		Argv: []string{"python3", "-c", code}, WorkingDir: "", TimeoutSeconds: 10, OutputLimitBytes: 4096,
	}, ResolvedEnvironment{Values: map[string]string{}})
	if err != nil || result.State != workspacev1.CommandTimedOut || !result.ProcessTreeTerminated {
		t.Fatalf("timeout result=%#v err=%v", result, err)
	}
	raw, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("read process group pid: %v stdout=%q stderr=%q", err, result.Stdout, result.Stderr)
	}
	pgid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		err = syscall.Kill(-pgid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("workspace command process group %d survived timeout: %v", pgid, err)
}
