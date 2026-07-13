package workspace_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keir-research/ai-native-paas/internal/source/application"
	"github.com/keir-research/ai-native-paas/internal/source/workspace"
)

func TestWorkspace_ApplyPatchRejectsPathTraversal(t *testing.T) {
	root := t.TempDir()
	g := workspace.Git{}
	for _, path := range []string{"../outside", "a/../../outside", "/absolute", ".git/config"} {
		if err := g.Apply(context.Background(), root, []application.PatchOperation{{Path: path, Content: []byte("x")}}); err == nil {
			t.Fatalf("accepted %q", path)
		}
	}
}
func TestWorkspace_ApplyPatchRejectsSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Skip(err)
	}
	g := workspace.Git{}
	if err := g.Apply(context.Background(), root, []application.PatchOperation{{Path: "link/owned", Content: []byte("x")}}); err == nil {
		t.Fatal("symlink escape accepted")
	}
	if _, err := os.Stat(filepath.Join(outside, "owned")); !os.IsNotExist(err) {
		t.Fatalf("outside file exists: %v", err)
	}
}
func TestWorkspace_ApplyPatchWritesAtomicallyAndSetsMode(t *testing.T) {
	root := t.TempDir()
	g := workspace.Git{}
	if err := g.Apply(context.Background(), root, []application.PatchOperation{{Path: "cmd/run.sh", Content: []byte("#!/bin/sh\n"), Executable: true}}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(root, "cmd/run.sh"))
	if err != nil || info.Mode()&0100 == 0 {
		t.Fatal(info, err)
	}
}
func TestWorkspace_ApplyPatchCanDeleteMissingFileIdempotently(t *testing.T) {
	root := t.TempDir()
	g := workspace.Git{}
	if err := g.Apply(context.Background(), root, []application.PatchOperation{{Path: "missing", Delete: true}}); err != nil {
		t.Fatal(err)
	}
}
func TestWorkspace_LocalGitAcceptanceExactSHACommitAndPush(t *testing.T) {
	requireGit(t)
	ctx := context.Background()
	remote := filepath.Join(t.TempDir(), "remote.git")
	run(t, "", "git", "init", "--bare", remote)
	seed := t.TempDir()
	run(t, seed, "git", "init")
	run(t, seed, "git", "config", "user.name", "Seed")
	run(t, seed, "git", "config", "user.email", "seed@example.test")
	if err := os.WriteFile(filepath.Join(seed, "README.md"), []byte("base\n"), 0644); err != nil {
		t.Fatal(err)
	}
	run(t, seed, "git", "add", "README.md")
	run(t, seed, "git", "commit", "-m", "base")
	base := strings.TrimSpace(run(t, seed, "git", "rev-parse", "HEAD"))
	run(t, seed, "git", "branch", "-M", "main")
	run(t, seed, "git", "remote", "add", "origin", remote)
	run(t, seed, "git", "push", "origin", "main")
	g := workspace.Git{Root: t.TempDir()}
	dir, err := g.CloneExact(ctx, remote, base, "agent-change", application.ProviderCredential{})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Cleanup(ctx, dir)
	if err = g.Apply(ctx, dir, []application.PatchOperation{{Path: "README.md", Content: []byte("changed\n")}, {Path: "src/main.txt", Content: []byte("hello\n")}}); err != nil {
		t.Fatal(err)
	}
	sha, err := g.Commit(ctx, dir, "agent change", "Agent", "agent@example.test")
	if err != nil {
		t.Fatal(err)
	}
	ok, err := g.Push(ctx, dir, "agent-change", "", sha, application.ProviderCredential{})
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	remoteSHA := strings.TrimSpace(run(t, "", "git", "--git-dir", remote, "rev-parse", "refs/heads/agent-change"))
	if remoteSHA != sha {
		t.Fatalf("remote=%s local=%s", remoteSHA, sha)
	}
}
func TestWorkspace_ForceWithLeaseRejectsUnexpectedRemoteHead(t *testing.T) {
	requireGit(t)
	ctx := context.Background()
	remote := filepath.Join(t.TempDir(), "remote.git")
	run(t, "", "git", "init", "--bare", remote)
	seed := t.TempDir()
	run(t, seed, "git", "init")
	run(t, seed, "git", "config", "user.name", "Seed")
	run(t, seed, "git", "config", "user.email", "seed@example.test")
	_ = os.WriteFile(filepath.Join(seed, "f"), []byte("1"), 0644)
	run(t, seed, "git", "add", "f")
	run(t, seed, "git", "commit", "-m", "one")
	base := strings.TrimSpace(run(t, seed, "git", "rev-parse", "HEAD"))
	run(t, seed, "git", "branch", "-M", "main")
	run(t, seed, "git", "remote", "add", "origin", remote)
	run(t, seed, "git", "push", "origin", "main")
	g := workspace.Git{Root: t.TempDir()}
	dir, err := g.CloneExact(ctx, remote, base, "main", application.ProviderCredential{})
	if err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(seed, "f"), []byte("2"), 0644)
	run(t, seed, "git", "add", "f")
	run(t, seed, "git", "commit", "-m", "two")
	run(t, seed, "git", "push", "origin", "main")
	if err = g.Apply(ctx, dir, []application.PatchOperation{{Path: "mine", Content: []byte("mine")}}); err != nil {
		t.Fatal(err)
	}
	mine, err := g.Commit(ctx, dir, "mine", "Agent", "a@b")
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := g.Push(ctx, dir, "main", base, mine, application.ProviderCredential{}); err == nil || ok {
		t.Fatalf("expected lease failure ok=%v err=%v", ok, err)
	}
}
func TestWorkspace_CredentialNeverWrittenIntoGitConfig(t *testing.T) {
	requireGit(t)
	ctx := context.Background()
	remote := filepath.Join(t.TempDir(), "remote.git")
	run(t, "", "git", "init", "--bare", remote)
	seed := t.TempDir()
	run(t, seed, "git", "init")
	run(t, seed, "git", "config", "user.name", "S")
	run(t, seed, "git", "config", "user.email", "s@e")
	_ = os.WriteFile(filepath.Join(seed, "f"), []byte("x"), 0644)
	run(t, seed, "git", "add", "f")
	run(t, seed, "git", "commit", "-m", "x")
	base := strings.TrimSpace(run(t, seed, "git", "rev-parse", "HEAD"))
	run(t, seed, "git", "branch", "-M", "main")
	run(t, seed, "git", "remote", "add", "origin", remote)
	run(t, seed, "git", "push", "origin", "main")
	g := workspace.Git{Root: t.TempDir()}
	dir, err := g.CloneExact(ctx, remote, base, "feature", application.ProviderCredential{Username: "bot", Token: "do-not-persist"})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, ".git", "config"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "do-not-persist") {
		t.Fatal("token persisted")
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.Contains(e.Name(), "askpass") {
			t.Fatalf("askpass file leaked: %s", e.Name())
		}
	}
}
func FuzzPatchPathCannotEscape(f *testing.F) {
	for _, s := range []string{"ok/file", "../x", "/x", ".git/config", "a/../../b", "x\x00y"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, path string) {
		root := t.TempDir()
		err := workspace.ValidatePatchPath(root, path)
		if err == nil {
			target := filepath.Clean(filepath.Join(root, path))
			rel, relErr := filepath.Rel(root, target)
			if relErr != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				t.Fatalf("escaped path accepted: %q", path)
			}
		}
	})
}
func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
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
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %v: %s", args, err, out)
	}
	return string(out)
}
