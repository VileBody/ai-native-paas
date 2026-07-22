package gitlab_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/keir-research/ai-native-paas/internal/source/application"
	"github.com/keir-research/ai-native-paas/internal/source/gitlab"
)

func TestGitLabReal_ProvisionCommitObserveArchive(t *testing.T) {
	if os.Getenv("GITLAB_LIVE") != "1" {
		t.Skip("set GITLAB_LIVE=1 for the destructive temporary-project gate")
	}
	token := strings.TrimSpace(os.Getenv("GITLAB_ADMIN_TOKEN"))
	namespaceID, err := strconv.ParseInt(strings.TrimSpace(os.Getenv("GITLAB_NAMESPACE_ID")), 10, 64)
	if token == "" || err != nil || namespaceID <= 0 {
		t.Fatal("GITLAB_ADMIN_TOKEN and a positive GITLAB_NAMESPACE_ID are required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	client := &gitlab.Client{
		BaseURL: "https://gitlab.com", AdminToken: token,
		MaxRateLimitRetries: 5, MaxRateLimitDelay: 30 * time.Second,
	}
	stamp := time.Now().UTC().Format("20060102-150405")
	slug := "paas-live-gate-" + stamp
	correlation := "gitlab-live-" + stamp
	repository, err := client.CreateRepository(ctx, application.CreateRepositoryRequest{
		NamespaceID: namespaceID, Name: "PaaS live gate " + stamp, Path: slug,
		DefaultBranch: "main", CorrelationID: correlation,
	})
	if err != nil {
		t.Fatal(err)
	}
	projectID := repository.ID
	credentialID := ""
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cleanupCancel()
		if credentialID != "" {
			_ = client.RevokeCredential(cleanupCtx, projectID, credentialID)
		}
		_ = client.DeleteRepository(cleanupCtx, projectID)
	})
	if repository.NamespaceID != namespaceID || repository.Path != slug || repository.DefaultBranch != "main" || repository.ExternalID != correlation {
		t.Fatalf("created repository identity differs from request: id=%d namespace=%d path=%q branch=%q", repository.ID, repository.NamespaceID, repository.Path, repository.DefaultBranch)
	}
	baseSHA, err := client.GetBranchHead(ctx, projectID, "main")
	if err != nil {
		t.Fatal(err)
	}
	commitSHA, err := client.BootstrapRepository(ctx, application.BootstrapRepositoryRequest{
		ProviderProjectID: projectID, Branch: "main", ExpectedBaseSHA: baseSHA,
		CommitMessage: "chore(platform): bootstrap controlled beta project\n\nPaaS-Correlation-ID: " + correlation,
		Files: []application.BootstrapFile{
			{Path: "platform.yaml", Content: []byte("apiVersion: platform.example.com/v2\nkind: Project\nmetadata:\n  name: live-gate\n")},
			{Path: "Dockerfile", Content: []byte("FROM scratch\nCOPY hello /hello\nENTRYPOINT [\"/hello\"]\n")},
			{Path: "deploy/base/kustomization.yaml", Content: []byte("apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\nresources: []\n")},
			{Path: "infrastructure/tofu/main.tf", Content: []byte("terraform { required_version = \">= 1.10.0\" }\n")},
			{Path: "recipes.lock.yaml", Content: []byte("apiVersion: platform.example.com/v1\nrecipes: []\n")},
		},
	})
	if err != nil || commitSHA == "" || strings.EqualFold(commitSHA, baseSHA) {
		t.Fatalf("bootstrap commit failed or did not advance head: %v", err)
	}
	if err := client.ProtectBranch(ctx, projectID, "main"); err != nil {
		t.Fatal(err)
	}
	found, ok, err := client.FindRepositoryByCorrelation(ctx, namespaceID, correlation)
	if err != nil || !ok || found.ID != projectID {
		t.Fatalf("lost-response correlation discovery failed: found=%t id=%d err=%v", ok, found.ID, err)
	}
	credential, err := client.CreateCredential(ctx, projectID, "workspace-live-gate", time.Now().UTC().Add(24*time.Hour))
	if err != nil || credential.ID == "" || credential.Token == "" || credential.ExpiresAt.IsZero() {
		t.Fatalf("project-scoped credential issuance failed: %v", err)
	}
	credentialID = credential.ID
	if err := client.RevokeCredential(ctx, projectID, credentialID); err != nil {
		t.Fatal(err)
	}
	credentialID = ""
	renamedPath := slug + "-renamed"
	renamed, err := client.RenameRepository(ctx, projectID, "PaaS renamed live gate "+stamp, renamedPath)
	if err != nil || renamed.ID != projectID || renamed.Path != renamedPath {
		t.Fatalf("repository rename failed: id=%d path=%q err=%v", renamed.ID, renamed.Path, err)
	}
	archived, err := client.ArchiveRepository(ctx, projectID)
	if err != nil || !archived.Archived {
		t.Fatalf("repository archive failed: %v", err)
	}
	restored, err := client.UnarchiveRepository(ctx, projectID)
	if err != nil || restored.Archived {
		t.Fatalf("repository restore failed: %v", err)
	}
	if err := client.DeleteRepository(ctx, projectID); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Minute)
	for time.Now().Before(deadline) {
		_, observeErr := client.GetRepository(ctx, projectID)
		var apiErr *gitlab.APIError
		if errors.As(observeErr, &apiErr) && apiErr.Status == http.StatusNotFound {
			projectID = 0
			return
		}
		if observeErr != nil {
			t.Fatal(observeErr)
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(time.Second):
		}
	}
	t.Fatal(fmt.Errorf("GitLab project %d was not deleted before the live-gate deadline", projectID))
}
