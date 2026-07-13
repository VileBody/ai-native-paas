package source_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keir-research/ai-native-paas/internal/build/application"
	"github.com/keir-research/ai-native-paas/internal/build/domain"
	buildsource "github.com/keir-research/ai-native-paas/internal/build/source"
	sourcev1 "github.com/keir-research/ai-native-paas/pkg/contracts/source/v1"
)

type resolver map[string]buildsource.RepositoryAccess

func (r resolver) ResolveRepository(_ context.Context, tenant, repo string) (buildsource.RepositoryAccess, error) {
	v, ok := r[repo]
	if !ok || v.TenantID != tenant {
		return buildsource.RepositoryAccess{}, domain.NewError(domain.CodeNotFound, "repository not found")
	}
	return v, nil
}
func gitRepo(t *testing.T, files map[string]string) (string, string) {
	t.Helper()
	remote := filepath.Join(t.TempDir(), "remote.git")
	run(t, "", "git", "init", "--bare", remote)
	work := t.TempDir()
	run(t, work, "git", "init")
	run(t, work, "git", "config", "user.name", "Test")
	run(t, work, "git", "config", "user.email", "test@example.test")
	for name, body := range files {
		path := filepath.Join(work, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if strings.HasPrefix(body, "SYMLINK:") {
			if err := os.Symlink(strings.TrimPrefix(body, "SYMLINK:"), path); err != nil {
				t.Fatal(err)
			}
		} else if err := os.WriteFile(path, []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	run(t, work, "git", "add", "--all")
	run(t, work, "git", "commit", "-m", "fixture")
	sha := strings.TrimSpace(run(t, work, "git", "rev-parse", "HEAD"))
	run(t, work, "git", "branch", "-M", "main")
	run(t, work, "git", "remote", "add", "origin", remote)
	run(t, work, "git", "push", "origin", "main")
	return remote, sha
}
func rev(repo, sha string) sourcev1.SourceRevision {
	return sourcev1.SourceRevision{ProjectID: "p1", RepositoryID: repo, Branch: "main", CommitSHA: sha}
}
func TestSourceFetcher_ChecksOutExactCommit(t *testing.T) {
	remote, sha := gitRepo(t, map[string]string{"go.mod": "module x", "main.go": "package main"})
	f := buildsource.Fetcher{Root: t.TempDir(), Resolver: resolver{"r1": {TenantID: "t1", RepositoryID: "r1", RemoteURL: remote}}}
	snap, err := f.Fetch(context.Background(), "t1", rev("r1", sha), application.SourceLimits{MaxBytes: 1 << 20, MaxFiles: 100})
	if err != nil {
		t.Fatal(err)
	}
	defer snap.Cleanup()
	if _, err := os.Stat(filepath.Join(snap.Path, "go.mod")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(snap.Path, ".git")); !os.IsNotExist(err) {
		t.Fatalf(".git remains: %v", err)
	}
}
func TestSourceFetcher_RejectsRepositorySizeLimit(t *testing.T) {
	remote, sha := gitRepo(t, map[string]string{"large": strings.Repeat("x", 1024)})
	f := buildsource.Fetcher{Resolver: resolver{"r": {TenantID: "t", RepositoryID: "r", RemoteURL: remote}}}
	_, err := f.Fetch(context.Background(), "t", rev("r", sha), application.SourceLimits{MaxBytes: 100})
	if !domain.HasCode(err, domain.CodeUserFailure) {
		t.Fatalf("err=%v", err)
	}
}
func TestSourceFetcher_RejectsSymlinkOutsideRoot(t *testing.T) {
	outside := filepath.Join(t.TempDir(), "outside")
	_ = os.WriteFile(outside, []byte("x"), 0644)
	remote, sha := gitRepo(t, map[string]string{"escape": "SYMLINK:" + outside})
	f := buildsource.Fetcher{Resolver: resolver{"r": {TenantID: "t", RepositoryID: "r", RemoteURL: remote}}}
	_, err := f.Fetch(context.Background(), "t", rev("r", sha), application.SourceLimits{})
	if !domain.HasCode(err, domain.CodeUserFailure) {
		t.Fatalf("err=%v", err)
	}
}
func TestSourceFetcher_SubmodulesDisabledByDefault(t *testing.T) {
	remote, sha := gitRepo(t, map[string]string{".gitmodules": "[submodule \"x\"]\npath=x\nurl=https://example.invalid/x"})
	f := buildsource.Fetcher{Resolver: resolver{"r": {TenantID: "t", RepositoryID: "r", RemoteURL: remote}}}
	_, err := f.Fetch(context.Background(), "t", rev("r", sha), application.SourceLimits{})
	if !domain.HasCode(err, domain.CodeUserFailure) {
		t.Fatalf("err=%v", err)
	}
}
func TestSourceFetcher_RemovesCredentialBeforeBuild(t *testing.T) {
	remote, sha := gitRepo(t, map[string]string{"x": "x"})
	f := buildsource.Fetcher{Resolver: resolver{"r": {TenantID: "t", RepositoryID: "r", RemoteURL: remote, Username: "bot", Token: "top-secret"}}}
	snap, err := f.Fetch(context.Background(), "t", rev("r", sha), application.SourceLimits{})
	if err != nil {
		t.Fatal(err)
	}
	defer snap.Cleanup()
	raw, _ := exec.Command("grep", "-R", "top-secret", snap.Path).CombinedOutput()
	if len(raw) > 0 {
		t.Fatalf("credential leaked: %s", raw)
	}
}
func TestSourceFetcher_DoesNotExposeOtherRepository(t *testing.T) {
	remote, sha := gitRepo(t, map[string]string{"x": "x"})
	f := buildsource.Fetcher{Resolver: resolver{"r": {TenantID: "other", RepositoryID: "r", RemoteURL: remote}}}
	_, err := f.Fetch(context.Background(), "t", rev("r", sha), application.SourceLimits{})
	if !domain.HasCode(err, domain.CodeNotFound) {
		t.Fatalf("err=%v", err)
	}
}
func run(t *testing.T, dir string, args ...string) string {
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

func TestSourceFetcher_RejectsSourceRootSymlinkEscape(t *testing.T) {
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("host-secret"), 0600); err != nil {
		t.Fatal(err)
	}
	remote, sha := gitRepo(t, map[string]string{"app": "SYMLINK:" + outside})
	f := buildsource.Fetcher{Resolver: resolver{"r": {TenantID: "t", RepositoryID: "r", RemoteURL: remote}}}
	revision := rev("r", sha)
	revision.SourceRoot = "app"
	_, err := f.Fetch(context.Background(), "t", revision, application.SourceLimits{})
	if !domain.HasCode(err, domain.CodeUserFailure) {
		t.Fatalf("err=%v", err)
	}
}
