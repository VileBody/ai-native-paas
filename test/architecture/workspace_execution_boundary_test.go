package architecture_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestArchitecture_WorkspaceManagerCannotExecuteControlPlaneShell(t *testing.T) {
	directory := filepath.Join(repositoryRoot(t), "internal", "workspace")
	err := filepath.WalkDir(directory, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		body := string(raw)
		for _, forbidden := range []string{`"os/exec"`, "exec.Command(", "exec.CommandContext("} {
			if strings.Contains(body, forbidden) {
				t.Errorf("workspace control plane can execute a local process: %s contains %s", path, forbidden)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
