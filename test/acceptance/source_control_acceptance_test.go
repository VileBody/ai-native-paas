package acceptance_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/keir-research/ai-native-paas/internal/source/application"
	"github.com/keir-research/ai-native-paas/internal/source/gitlab"
	"github.com/keir-research/ai-native-paas/internal/source/memory"
	"github.com/keir-research/ai-native-paas/internal/source/support"
	"github.com/keir-research/ai-native-paas/internal/source/testkit"
	sourcehook "github.com/keir-research/ai-native-paas/internal/source/webhook"
	"github.com/keir-research/ai-native-paas/internal/source/workspace"
)

type gitLabStub struct {
	mu                                       sync.Mutex
	head                                     string
	created, protected, credentials, revoked int
	correlation                              string
}

func (s *gitLabStub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	path := strings.TrimPrefix(r.URL.Path, "/api/v4")
	switch {
	case r.Method == http.MethodPost && path == "/projects":
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		s.created++
		s.correlation = fmt.Sprint(body["description"])
		_, _ = io.WriteString(w, `{"id":42,"namespace":{"id":7},"path":"booking","path_with_namespace":"acme/booking","web_url":"https://git.example/acme/booking","default_branch":"main","description":"`+s.correlation+`"}`)
	case r.Method == http.MethodPost && path == "/projects/42/protected_branches":
		s.protected++
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{}`)
	case r.Method == http.MethodPost && path == "/projects/42/access_tokens":
		s.credentials++
		_, _ = io.WriteString(w, `{"id":9,"user_name":"project_42_bot","token":"ephemeral-only","expires_at":"2026-07-13"}`)
	case r.Method == http.MethodDelete && path == "/projects/42/access_tokens/9":
		s.revoked++
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodGet && path == "/projects/42":
		_, _ = io.WriteString(w, `{"id":42,"namespace":{"id":7},"path":"booking","path_with_namespace":"acme/booking","web_url":"https://git.example/acme/booking","default_branch":"main"}`)
	case r.Method == http.MethodGet && strings.HasPrefix(path, "/projects/42/repository/branches/"):
		_, _ = io.WriteString(w, `{"commit":{"id":"`+s.head+`"}}`)
	case r.Method == http.MethodGet && path == "/groups/7/projects":
		_, _ = io.WriteString(w, `[{"id":42,"namespace":{"id":7},"path":"booking","path_with_namespace":"acme/booking","web_url":"https://git.example/acme/booking","default_branch":"main","description":"`+s.correlation+`"}]`)
	default:
		http.Error(w, "unexpected "+r.Method+" "+path, http.StatusNotFound)
	}
}
func TestAcceptance_GitLabWorkspaceWebhookAndDedupe(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	ctx := context.Background()
	remote := filepath.Join(t.TempDir(), "remote.git")
	runGit(t, "", "git", "init", "--bare", remote)
	seed := t.TempDir()
	runGit(t, seed, "git", "init")
	runGit(t, seed, "git", "config", "user.name", "Seed")
	runGit(t, seed, "git", "config", "user.email", "seed@example.test")
	_ = os.WriteFile(filepath.Join(seed, "main.go"), []byte("package main\n"), 0644)
	runGit(t, seed, "git", "add", "main.go")
	runGit(t, seed, "git", "commit", "-m", "base")
	base := strings.TrimSpace(runGit(t, seed, "git", "rev-parse", "HEAD"))
	runGit(t, seed, "git", "branch", "-M", "main")
	runGit(t, seed, "git", "remote", "add", "origin", remote)
	runGit(t, seed, "git", "push", "origin", "main")
	stub := &gitLabStub{head: base}
	server := httptest.NewServer(stub)
	defer server.Close()
	store := memory.New()
	clock := &testkit.Clock{T: time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)}
	ids := &support.IDs{}
	provider := &gitlab.Client{BaseURL: server.URL, AdminToken: "admin"}
	source := &application.Service{Store: store, Provider: provider, Clock: clock, IDs: ids}
	created, err := source.CreateProject(ctx, application.CreateProjectCommand{TenantID: "t1", ActorID: "agent1", Name: "Booking", ProviderNamespaceID: 7, IdempotencyKey: "create-1"})
	if err != nil {
		t.Fatal(err)
	}
	repo, err := source.ProvisionRepository(ctx, application.ProvisionRepositoryCommand{TenantID: "t1", ActorID: "agent1", RepositoryID: created.Repository.ID})
	if err != nil {
		t.Fatal(err)
	}
	wsSvc := &application.WorkspaceService{Store: store, Provider: provider, Git: workspace.Git{Root: t.TempDir()}, Clock: clock, IDs: ids}
	ws, err := wsSvc.Create(ctx, application.CreateWorkspaceCommand{TenantID: "t1", ActorID: "agent1", RepositoryID: repo.ID, Branch: "agent-feature", BaseSHA: base, TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	ws, err = wsSvc.Execute(ctx, application.ExecuteWorkspaceCommand{TenantID: "t1", ActorID: "agent1", WorkspaceID: ws.ID, RemoteURL: remote, Message: "agent implementation", AuthorName: "Agent", AuthorEmail: "agent@example.test", Patch: []application.PatchOperation{{Path: "main.go", Content: []byte("package main\n\nfunc main() {}\n")}}})
	if err != nil {
		t.Fatal(err)
	}
	if ws.CommitSHA == "" {
		t.Fatal("commit sha missing")
	}
	stub.mu.Lock()
	stub.head = ws.CommitSHA
	stub.mu.Unlock()
	raw := []byte(fmt.Sprintf(`{"object_kind":"push","before":"%s","after":"%s","ref":"refs/heads/agent-feature","project":{"id":42}}`, base, ws.CommitSHA))
	ts, sig := sourcehook.Sign([]byte("hook-secret"), "evt-1", clock.Now(), raw)
	headers := map[string][]string{"webhook-id": {"evt-1"}, "webhook-timestamp": {ts}, "webhook-signature": {sig}}
	hooks := application.WebhookService{Store: store, Provider: provider, Verifier: sourcehook.Verifier{Secret: []byte("hook-secret")}, Normalizer: sourcehook.Normalizer{}, Clock: clock, IDs: ids}
	first, err := hooks.Handle(ctx, "t1", headers, raw)
	if err != nil || first.Duplicate {
		t.Fatal(first, err)
	}
	second, err := hooks.Handle(ctx, "t1", headers, raw)
	if err != nil || !second.Duplicate {
		t.Fatal(second, err)
	}
	stub.mu.Lock()
	defer stub.mu.Unlock()
	if stub.created != 1 || stub.protected != 1 || stub.credentials != 1 || stub.revoked != 1 {
		t.Fatalf("stub create=%d protect=%d cred=%d revoke=%d", stub.created, stub.protected, stub.credentials, stub.revoked)
	}
	remoteSHA := strings.TrimSpace(runGit(t, "", "git", "--git-dir", remote, "rev-parse", "refs/heads/agent-feature"))
	if remoteSHA != ws.CommitSHA {
		t.Fatalf("remote=%s workspace=%s", remoteSHA, ws.CommitSHA)
	}
}
func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command(args[0], args[1:]...)
	if dir == "" {
		dir = os.TempDir()
	}
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull)
	raw, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %v: %s", args, err, raw)
	}
	return string(raw)
}

var _ = strconv.Itoa
