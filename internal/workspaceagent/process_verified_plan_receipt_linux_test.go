//go:build linux

package workspaceagent

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestVerifiedTofuPlan_RejectsMovedOrDirtySourceRevision(t *testing.T) {
	repository := t.TempDir()
	runGitForSourceTest(t, repository, "init", "--quiet")
	runGitForSourceTest(t, repository, "config", "user.name", "Workspace Test")
	runGitForSourceTest(t, repository, "config", "user.email", "workspace@test.invalid")
	tracked := filepath.Join(repository, "main.tf")
	if err := os.WriteFile(tracked, []byte("terraform {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGitForSourceTest(t, repository, "add", "main.tf")
	runGitForSourceTest(t, repository, "commit", "--quiet", "-m", "exact revision")
	head := strings.TrimSpace(runGitForSourceTest(t, repository, "rev-parse", "HEAD"))

	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chdir(repository); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(previous) })
	if err = verifyWorkspaceSource(head, "saved.plan"); err != nil {
		t.Fatalf("exact clean source rejected: %v", err)
	}
	if err = os.WriteFile(filepath.Join(repository, "saved.plan"), []byte("previous plan"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err = verifyWorkspaceSource(head, "saved.plan"); err != nil {
		t.Fatalf("idempotent untracked plan artifact rejected: %v", err)
	}
	if err = verifyWorkspaceSource(strings.Repeat("f", 40), "saved.plan"); err == nil {
		t.Fatal("moved source revision accepted")
	}
	if err = os.WriteFile(tracked, []byte("terraform { required_version = \">= 1.0\" }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err = verifyWorkspaceSource(head, "saved.plan"); err == nil {
		t.Fatal("dirty source tree accepted")
	}
}

func runGitForSourceTest(t *testing.T, directory string, arguments ...string) string {
	t.Helper()
	command := exec.Command("git", arguments...)
	command.Dir = directory
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", arguments[0], err, output)
	}
	return string(output)
}
