package architecture_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestArchitecture_WorkspaceAgentIsOutboundOnlyAndExecIsIsolatedToExecutor(t *testing.T) {
	root := repositoryRoot(t)
	directories := []string{filepath.Join(root, "cmd", "workspace-agent"), filepath.Join(root, "internal", "workspaceagent")}
	for _, directory := range directories {
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
			for _, forbidden := range []string{"net.Listen(", "http.ListenAndServe(", "http.Serve(", "ssh.Listen("} {
				if filepath.Base(path) == "local_proxy.go" && forbidden == "net.Listen(" {
					continue
				}
				if strings.Contains(body, forbidden) {
					t.Errorf("workspace agent exposes an inbound listener: %s contains %s", path, forbidden)
				}
			}
			if filepath.Base(path) == "local_proxy.go" && (!strings.Contains(body, "IsLoopback()") || !strings.Contains(body, "net.SplitHostPort(address)")) {
				t.Errorf("workspace local proxy is not constrained to a parsed loopback address: %s", path)
			}
			trustedExecutors := map[string]struct{}{"executor.go": {}, "verified_git.go": {}}
			_, trustedExecutor := trustedExecutors[filepath.Base(path)]
			if strings.Contains(body, `"os/exec"`) && !trustedExecutor && !strings.HasPrefix(filepath.Base(path), "process_") {
				t.Errorf("workspace command execution escaped the isolated executor: %s", path)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}
