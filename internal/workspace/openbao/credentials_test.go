package openbao

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/keir-research/ai-native-paas/internal/workspace"
)

func TestOpenBaoCredentials_ResolveExactCommandScopedReferences(t *testing.T) {
	now := time.Date(2026, 7, 14, 18, 0, 0, 0, time.UTC)
	reference := "credential://gitlab-project-1"
	digest := sha256.Sum256([]byte(reference))
	wantedPath := "/v1/workspace-credentials/data/tenant-1/project-1/workspace-1/" + hex.EncodeToString(digest[:])
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.URL.Path != wantedPath || request.Header.Get("Authorization") != "Bearer "+openBaoTokenSentinel {
			http.Error(response, "unexpected request", http.StatusForbidden)
			return
		}
		_ = json.NewEncoder(response).Encode(map[string]any{"data": map[string]any{"data": map[string]any{
			"value": "credential-sentinel-never-log", "expires_at": now.Add(10 * time.Minute),
			"tenant_id": "tenant-1", "project_id": "project-1", "workspace_id": "workspace-1", "task_id": "task-1",
			"command_id": "command-1", "reference": reference,
		}}})
	}))
	defer server.Close()
	source, err := NewCredentialSource(CredentialConfig{
		Address: server.URL, TokenFile: tokenFile(t), Mount: "workspace-credentials", MaximumTTL: 15 * time.Minute,
		HTTPClient: server.Client(), Clock: issuerClock{now},
	})
	if err != nil {
		t.Fatal(err)
	}
	view, err := source.Resolve(context.Background(), workspace.CredentialSourceRequest{
		TenantID: "tenant-1", ProjectID: "project-1", WorkspaceID: "workspace-1", TaskID: "task-1", CommandID: "command-1",
		AgentSessionID: "session-1", VMID: "vm-1", EnvironmentRefs: map[string]string{"GITLAB_TOKEN": reference},
		CredentialLeases: []string{"gitlab-project-1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if view.Values["GITLAB_TOKEN"] != "credential-sentinel-never-log" || !view.ExpiresAt.Equal(now.Add(10*time.Minute)) {
		t.Fatalf("credential view=%#v", view)
	}
}

func TestOpenBaoCredentials_RejectsUnboundAndUnusedLeasesWithoutLeakingValue(t *testing.T) {
	now := time.Date(2026, 7, 14, 18, 0, 0, 0, time.UTC)
	source, err := NewCredentialSource(CredentialConfig{
		Address: "http://127.0.0.1:1", TokenFile: tokenFile(t), Mount: "workspace-credentials", MaximumTTL: 15 * time.Minute,
		HTTPClient: &http.Client{}, Clock: issuerClock{now},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = source.Resolve(context.Background(), workspace.CredentialSourceRequest{
		TenantID: "tenant-1", ProjectID: "project-1", WorkspaceID: "workspace-1", TaskID: "task-1", CommandID: "command-1",
		AgentSessionID: "session-1", VMID: "vm-1", EnvironmentRefs: map[string]string{"TOKEN": "credential://attacker-lease"},
		CredentialLeases: []string{"allowed-lease"},
	})
	if err == nil || strings.Contains(err.Error(), openBaoTokenSentinel) {
		t.Fatalf("unsafe credential error=%v", err)
	}
}
