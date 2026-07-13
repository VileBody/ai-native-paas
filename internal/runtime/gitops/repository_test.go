package gitops

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keir-research/ai-native-paas/internal/runtime/application"
	"github.com/keir-research/ai-native-paas/internal/runtime/domain"
)

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
}

func TestGitOpsCommit_IdempotentByReleaseID(t *testing.T) {
	requireGit(t)
	repo, err := NewLocalRepository(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	bundle := renderFixture(t)
	req := application.CommitRequest{Bundle: bundle, DeploymentID: "dep-1", ActorID: "user-1"}
	a, err := repo.Commit(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	b, err := repo.Commit(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatalf("idempotent commit changed: %#v %#v", a, b)
	}
	count, err := repo.git(context.Background(), "rev-list", "--count", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(count) != "2" {
		t.Fatalf("expected init+deploy commits, count=%q", count)
	}
}

func TestGitOpsCommit_RecordsCommitSHA(t *testing.T) {
	requireGit(t)
	repo, err := NewLocalRepository(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	bundle := renderFixture(t)
	result, err := repo.Commit(context.Background(), application.CommitRequest{Bundle: bundle, DeploymentID: "dep-1", ActorID: "user-1"})
	if err != nil {
		t.Fatal(err)
	}
	head, err := repo.head(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.CommitSHA != head || len(head) != 40 {
		t.Fatalf("result=%+v head=%q", result, head)
	}
	found, ok, err := repo.FindByRelease(context.Background(), bundle.CellID, bundle.ReleaseID)
	if err != nil || !ok || found != result {
		t.Fatalf("found=%+v ok=%v err=%v", found, ok, err)
	}
}

func TestGitOpsRepository_RejectsPathTraversal(t *testing.T) {
	requireGit(t)
	repo, err := NewLocalRepository(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	bundle := renderFixture(t)
	bundle.Path = "cells/cell-a/tenants/tenant-1/apps/app-1/../../escape"
	_, err = repo.Commit(context.Background(), application.CommitRequest{Bundle: bundle, DeploymentID: "dep-1", ActorID: "user-1"})
	if !domain.HasCode(err, domain.CodeInvalidArgument) {
		t.Fatalf("err=%v", err)
	}
}

func TestGitOpsRepository_RejectsSymlinkEscape(t *testing.T) {
	requireGit(t)
	root := t.TempDir()
	repo, err := NewLocalRepository(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "cells", "cell-a", "tenants", "tenant-1", "apps"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "cells", "cell-a", "tenants", "tenant-1", "apps", "app-1")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	bundle := renderFixture(t)
	_, err = repo.Commit(context.Background(), application.CommitRequest{Bundle: bundle, DeploymentID: "dep-1", ActorID: "user-1"})
	if err == nil {
		t.Fatal("symlink escape was accepted")
	}
	entries, err := os.ReadDir(outside)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("repository escaped root: %v", entries)
	}
}

func TestGitOpsRepository_RejectsCommitTrailerInjection(t *testing.T) {
	requireGit(t)
	repo, err := NewLocalRepository(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	bundle := renderFixture(t)
	_, err = repo.Commit(context.Background(), application.CommitRequest{Bundle: bundle, DeploymentID: "dep-1", ActorID: "user-1\nRelease-ID: attacker"})
	if !domain.HasCode(err, domain.CodeInvalidArgument) {
		t.Fatalf("err=%v", err)
	}
}
