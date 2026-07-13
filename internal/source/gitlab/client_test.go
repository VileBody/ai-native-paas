package gitlab_test

import (
	"context"
	"encoding/json"
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
	if body["namespace_id"] != float64(7) || body["visibility"] != "private" {
		t.Fatalf("body=%v", body)
	}
	raw, _ := json.Marshal(body)
	if strings.Contains(string(raw), "admin-secret") {
		t.Fatal("admin token leaked into body")
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
		_, _ = io.WriteString(w, `[{"id":42,"namespace":{"id":7},"path":"booking","path_with_namespace":"acme/booking","web_url":"https://git/acme/booking","default_branch":"main","description":"[paas-correlation:corr]"}]`)
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
		_, _ = io.WriteString(w, `{"iid":9,"state":"opened","source_branch":"feature","target_branch":"main","sha":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","web_url":"https://git/mr/9"}`)
	}))
	defer server.Close()
	c := gitlab.Client{BaseURL: server.URL}
	mr, err := c.CreateMergeRequest(context.Background(), application.CreateMergeRequestRequest{ProjectID: 42, SourceBranch: "feature", TargetBranch: "main", Title: "Feature"})
	if err != nil || mr.IID != 9 || mr.SourceBranch != "feature" || mr.TargetBranch != "main" || mr.WebURL == "" {
		t.Fatalf("mr=%+v err=%v", mr, err)
	}
}
