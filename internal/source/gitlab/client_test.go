package gitlab_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/keir-research/ai-native-paas/internal/source/application"
	"github.com/keir-research/ai-native-paas/internal/source/gitlab"
)

func TestGitLab_CreateRepositoryUsesNumericNamespaceAndPrivateVisibility(t *testing.T) {
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("PRIVATE-TOKEN") != "admin-secret" {
			t.Errorf("token header missing")
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":42,"namespace":{"id":7},"path":"booking","path_with_namespace":"acme/booking","web_url":"https://git/acme/booking","default_branch":"main","description":"[paas-correlation:corr]"}`)
	}))
	defer server.Close()
	c := gitlab.Client{BaseURL: server.URL, AdminToken: "admin-secret"}
	repo, err := c.CreateRepository(context.Background(), application.CreateRepositoryRequest{NamespaceID: 7, Name: "Booking", Path: "booking", DefaultBranch: "main", CorrelationID: "corr"})
	if err != nil || repo.ID != 42 {
		t.Fatal(repo, err)
	}
	topics, topicsOK := body["topics"].([]any)
	if body["namespace_id"] != float64(7) || body["visibility"] != "private" || !topicsOK || len(topics) != 1 || topics[0] != "ai-native-paas" {
		t.Fatalf("body=%v", body)
	}
	raw, _ := json.Marshal(body)
	if strings.Contains(string(raw), "admin-secret") {
		t.Fatal("admin token leaked into body")
	}
}
func TestGitLab_ListNamespaceRepositoriesReturnsManagedIdentityWithoutSharedProjects(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v4/groups/7/projects" || r.URL.Query().Get("with_shared") != "false" || r.URL.Query().Get("include_subgroups") != "false" || r.URL.Query().Get("per_page") != "100" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.String())
		}
		_, _ = io.WriteString(w, `[
          {"id":42,"namespace":{"id":7},"path":"booking","path_with_namespace":"acme/booking","web_url":"https://git/acme/booking","default_branch":"main","description":"[paas-correlation:corr-42]","topics":["ai-native-paas"]},
          {"id":99,"namespace":{"id":8},"path":"shared","path_with_namespace":"other/shared","description":"[paas-correlation:other]","topics":["ai-native-paas"]}
        ]`)
	}))
	defer server.Close()
	c := gitlab.Client{BaseURL: server.URL}
	repositories, err := c.ListRepositoriesByNamespace(context.Background(), 7)
	if err != nil || len(repositories) != 1 {
		t.Fatalf("repositories=%+v err=%v", repositories, err)
	}
	repository := repositories[0]
	if repository.ID != 42 || repository.ExternalID != "corr-42" || len(repository.Topics) != 1 || repository.Topics[0] != "ai-native-paas" {
		t.Fatalf("repository=%+v", repository)
	}
}
func TestGitLab_CreateConflictRecoversByCorrelationMarker(t *testing.T) {
	var mu sync.Mutex
	calls := map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls[r.Method+" "+r.URL.Path]++
		mu.Unlock()
		if r.Method == http.MethodPost {
			http.Error(w, `{"message":"has already been taken"}`, http.StatusConflict)
			return
		}
		_, _ = io.WriteString(w, `[{"id":42,"namespace":{"id":7},"path":"booking","path_with_namespace":"acme/booking","web_url":"https://git/acme/booking","default_branch":"main","description":"[paas-correlation:corr]","topics":["ai-native-paas"]}]`)
	}))
	defer server.Close()
	c := gitlab.Client{BaseURL: server.URL}
	repo, err := c.CreateRepository(context.Background(), application.CreateRepositoryRequest{NamespaceID: 7, Name: "Booking", Path: "booking", DefaultBranch: "main", CorrelationID: "corr"})
	if err != nil || repo.ID != 42 {
		t.Fatal(repo, err)
	}
}
func TestGitLab_ProtectBranchConflictIsIdempotent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "exists", http.StatusConflict) }))
	defer server.Close()
	c := gitlab.Client{BaseURL: server.URL}
	if err := c.ProtectBranch(context.Background(), 42, "main"); err != nil {
		t.Fatal(err)
	}
}
func TestGitLab_BranchNameIsPathEscaped(t *testing.T) {
	var path string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.EscapedPath()
		_, _ = io.WriteString(w, `{"commit":{"id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}`)
	}))
	defer server.Close()
	c := gitlab.Client{BaseURL: server.URL}
	_, err := c.GetBranchHead(context.Background(), 42, "feature/payments")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(path, "feature%2Fpayments") {
		t.Fatalf("path=%s", path)
	}
}
func TestGitLab_ErrorRedactsAdminToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "admin-secret", http.StatusInternalServerError)
	}))
	defer server.Close()
	c := gitlab.Client{BaseURL: server.URL, AdminToken: "admin-secret"}
	_, err := c.GetRepository(context.Background(), 42)
	if err == nil || strings.Contains(err.Error(), "admin-secret") {
		t.Fatalf("err=%v", err)
	}
}

func TestGitLab_RateLimitRetryIsBoundedAndHonorsRetryAfter(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Retry-After", "50")
		http.Error(w, "retry later", http.StatusTooManyRequests)
	}))
	defer server.Close()
	var delays []time.Duration
	client := gitlab.Client{
		BaseURL: server.URL, MaxRateLimitRetries: 2, MaxRateLimitDelay: 2 * time.Second,
		Sleep: func(_ context.Context, delay time.Duration) error { delays = append(delays, delay); return nil },
	}
	_, err := client.GetRepository(context.Background(), 42)
	var apiErr *gitlab.APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusTooManyRequests || calls != 3 || len(delays) != 2 {
		t.Fatalf("calls=%d delays=%v err=%v", calls, delays, err)
	}
	for _, delay := range delays {
		if delay != 2*time.Second {
			t.Fatalf("delay was not capped: %s", delay)
		}
	}
}

func TestGitLab_NonIdempotentPostIsNotRetriedAfter429(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Retry-After", "1")
		http.Error(w, "retry later", http.StatusTooManyRequests)
	}))
	defer server.Close()
	sleeps := 0
	client := gitlab.Client{BaseURL: server.URL, Sleep: func(context.Context, time.Duration) error { sleeps++; return nil }}
	_, err := client.CreateRepository(context.Background(), application.CreateRepositoryRequest{NamespaceID: 7, Name: "Booking", Path: "booking", DefaultBranch: "main", CorrelationID: "corr"})
	if err == nil || calls != 1 || sleeps != 0 {
		t.Fatalf("calls=%d sleeps=%d err=%v", calls, sleeps, err)
	}
}
func TestGitLab_RevokeMissingCredentialIsIdempotent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) }))
	defer server.Close()
	c := gitlab.Client{BaseURL: server.URL}
	if err := c.RevokeCredential(context.Background(), 42, "7"); err != nil {
		t.Fatal(err)
	}
}
func TestGitLab_CreateCredentialReturnsTokenOnlyFromResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"id":7,"user_name":"project_42_bot","token":"ephemeral","expires_at":"2026-07-13"}`)
	}))
	defer server.Close()
	c := gitlab.Client{BaseURL: server.URL}
	cred, err := c.CreateCredential(context.Background(), 42, "workspace", time.Now().Add(time.Hour))
	if err != nil || cred.ID != "7" || cred.Token != "ephemeral" || cred.Username != "project_42_bot" {
		t.Fatal(cred, err)
	}
}
func TestGitLab_CreateMergeRequestMapsSnakeCaseFields(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"iid":9,"state":"opened","source_branch":"feature","target_branch":"main","sha":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","web_url":"https://git/mr/9","description":"governed"}`)
	}))
	defer server.Close()
	c := gitlab.Client{BaseURL: server.URL}
	mr, err := c.CreateMergeRequest(context.Background(), application.CreateMergeRequestRequest{ProjectID: 42, SourceBranch: "feature", TargetBranch: "main", Title: "Feature"})
	if err != nil || mr.IID != 9 || mr.SourceBranch != "feature" || mr.TargetBranch != "main" || mr.WebURL == "" {
		t.Fatalf("mr=%+v err=%v", mr, err)
	}
}

func TestGitLab_FindOpenMergeRequestUsesExactSourceAndTarget(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v4/projects/42/merge_requests" || r.URL.Query().Get("state") != "opened" || r.URL.Query().Get("source_branch") != "agent/task-1" || r.URL.Query().Get("target_branch") != "main" {
			t.Fatalf("unexpected request %s %s?%s", r.Method, r.URL.Path, r.URL.RawQuery)
		}
		_, _ = io.WriteString(w, `[{"iid":9,"state":"opened","source_branch":"agent/task-1","target_branch":"main","sha":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","web_url":"https://git/mr/9","description":"governed"}]`)
	}))
	defer server.Close()
	c := gitlab.Client{BaseURL: server.URL}
	mr, found, err := c.FindOpenMergeRequest(context.Background(), 42, "agent/task-1", "main")
	if err != nil || !found || mr.IID != 9 || mr.HeadSHA != strings.Repeat("b", 40) {
		t.Fatalf("mr=%+v found=%v err=%v", mr, found, err)
	}
}

func TestGitLab_CreateAndFindMergeRequestNoteUsesNotesAPI(t *testing.T) {
	marker := "<!-- ai-native-paas-plan-summary:v1 sha256:" + strings.Repeat("a", 64) + " -->"
	body := marker + "\n### Platform plan summary"
	posts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v4/projects/42/merge_requests/9/notes" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		switch r.Method {
		case http.MethodPost:
			posts++
			var request map[string]string
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Fatal(err)
			}
			if request["body"] != body {
				t.Fatalf("body=%q", request["body"])
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"id": 17, "body": body})
		case http.MethodGet:
			if r.URL.Query().Get("sort") != "desc" || r.URL.Query().Get("order_by") != "created_at" || r.URL.Query().Get("per_page") != "100" {
				t.Fatalf("query=%s", r.URL.RawQuery)
			}
			_ = json.NewEncoder(w).Encode([]map[string]any{{"id": 17, "body": body}})
		default:
			t.Fatalf("unexpected method %s", r.Method)
		}
	}))
	defer server.Close()
	client := gitlab.Client{BaseURL: server.URL}
	created, err := client.CreateMergeRequestNote(context.Background(), 42, 9, body)
	if err != nil || created.ID != 17 || created.Body != body || posts != 1 {
		t.Fatalf("created=%+v posts=%d err=%v", created, posts, err)
	}
	found, ok, err := client.FindMergeRequestNoteByMarker(context.Background(), 42, 9, marker)
	if err != nil || !ok || found != created {
		t.Fatalf("found=%+v ok=%v err=%v", found, ok, err)
	}
}

func TestGitLab_ArchiveRestoreAndDeleteUseProjectLifecycleAPI(t *testing.T) {
	var actions []string
	deleteCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		actions = append(actions, r.Method+" "+r.URL.Path)
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v4/projects/42/archive":
			_, _ = io.WriteString(w, `{"id":42,"namespace":{"id":7},"path":"booking","path_with_namespace":"acme/booking","web_url":"https://git/acme/booking","default_branch":"main","archived":true}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v4/projects/42/unarchive":
			_, _ = io.WriteString(w, `{"id":42,"namespace":{"id":7},"path":"booking","path_with_namespace":"acme/booking","web_url":"https://git/acme/booking","default_branch":"main","archived":false}`)
		case r.Method == http.MethodDelete && r.URL.Path == "/api/v4/projects/42":
			deleteCalls++
			if deleteCalls > 1 {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.WriteHeader(http.StatusAccepted)
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()
	client := gitlab.Client{BaseURL: server.URL}
	archived, err := client.ArchiveRepository(context.Background(), 42)
	if err != nil || !archived.Archived || archived.ID != 42 {
		t.Fatalf("archived=%+v err=%v", archived, err)
	}
	restored, err := client.UnarchiveRepository(context.Background(), 42)
	if err != nil || restored.Archived || restored.ID != 42 {
		t.Fatalf("restored=%+v err=%v", restored, err)
	}
	if err := client.DeleteRepository(context.Background(), 42); err != nil {
		t.Fatal(err)
	}
	if err := client.DeleteRepository(context.Background(), 42); err != nil {
		t.Fatalf("idempotent delete after provider 404: %v", err)
	}
	if len(actions) != 4 {
		t.Fatalf("actions=%v", actions)
	}
}

func TestGitLab_RenameUsesStableNumericProjectIdentity(t *testing.T) {
	var body map[string]string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || r.URL.Path != "/api/v4/projects/42" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		_, _ = io.WriteString(w, `{"id":42,"name":"Reservations","namespace":{"id":7},"path":"reservations","path_with_namespace":"acme/reservations","web_url":"https://git/acme/reservations","default_branch":"main"}`)
	}))
	defer server.Close()
	client := gitlab.Client{BaseURL: server.URL}
	repository, err := client.RenameRepository(context.Background(), 42, "Reservations", "reservations")
	if err != nil || repository.ID != 42 || repository.PathWithNamespace != "acme/reservations" || body["name"] != "Reservations" || body["path"] != "reservations" {
		t.Fatalf("repository=%+v body=%+v err=%v", repository, body, err)
	}
}

func TestGitLab_BootstrapRepositoryUsesExactBaseAndBatchCommit(t *testing.T) {
	var body struct {
		Branch        string `json:"branch"`
		StartSHA      string `json:"start_sha"`
		CommitMessage string `json:"commit_message"`
		Actions       []struct {
			Action   string `json:"action"`
			FilePath string `json:"file_path"`
			Content  string `json:"content"`
			Encoding string `json:"encoding"`
		} `json:"actions"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v4/projects/42/repository/commits" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		_, _ = io.WriteString(w, `{"id":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}`)
	}))
	defer server.Close()
	client := gitlab.Client{BaseURL: server.URL}
	commit, err := client.BootstrapRepository(context.Background(), application.BootstrapRepositoryRequest{
		ProviderProjectID: 42, Branch: "main", ExpectedBaseSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		CommitMessage: "Initialize platform project\n\nPaaS-Correlation: corr-1",
		Files:         []application.BootstrapFile{{Path: "README.md", Content: []byte("hello\n"), Update: true}, {Path: "platform.yaml", Content: []byte("kind: Project\n")}},
	})
	if err != nil || commit != "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" {
		t.Fatalf("commit=%q err=%v", commit, err)
	}
	if body.Branch != "main" || body.StartSHA != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" || len(body.Actions) != 2 || body.Actions[0].Action != "update" || body.Actions[1].Action != "create" {
		t.Fatalf("body=%+v", body)
	}
	decoded, err := base64.StdEncoding.DecodeString(body.Actions[1].Content)
	if err != nil || string(decoded) != "kind: Project\n" || body.Actions[1].Encoding != "base64" {
		t.Fatalf("decoded=%q action=%+v err=%v", decoded, body.Actions[1], err)
	}
}

func TestGitLab_BootstrapLostResponseDiscoversExactFiles(t *testing.T) {
	content := []byte("apiVersion: platform.example.com/v2\n")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/repository/commits"):
			http.Error(w, `{"message":"file already exists"}`, http.StatusBadRequest)
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/repository/files/"):
			_ = json.NewEncoder(w).Encode(map[string]string{"encoding": "base64", "content": base64.StdEncoding.EncodeToString(content)})
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/repository/branches/"):
			_, _ = io.WriteString(w, `{"commit":{"id":"cccccccccccccccccccccccccccccccccccccccc"}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client := gitlab.Client{BaseURL: server.URL}
	commit, err := client.BootstrapRepository(context.Background(), application.BootstrapRepositoryRequest{
		ProviderProjectID: 42, Branch: "main", ExpectedBaseSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		CommitMessage: "Initialize", Files: []application.BootstrapFile{{Path: "platform.yaml", Content: content}},
	})
	if err != nil || commit != "cccccccccccccccccccccccccccccccccccccccc" {
		t.Fatalf("commit=%q err=%v", commit, err)
	}
}
