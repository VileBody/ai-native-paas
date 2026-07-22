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
			trustedExecutors := map[string]struct{}{"executor.go": {}, "verified_build.go": {}, "verified_git.go": {}}
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

func TestArchitecture_WorkspaceSupervisorAndTaskUseSeparateUnixIdentities(t *testing.T) {
	root := repositoryRoot(t)
	agentUnit := readArchitectureFile(t, filepath.Join(root, "infra", "images", "workspace", "ai-native-paas-workspace-agent.service"))
	for _, required := range []string{
		"User=root", "Group=workspace-agent", "SupplementaryGroups=workspace-shared",
		"CapabilityBoundingSet=CAP_SETUID CAP_SETGID", "NoNewPrivileges=yes", "ProtectProc=invisible",
	} {
		if !strings.Contains(agentUnit, required) {
			t.Errorf("workspace supervisor unit lacks %q", required)
		}
	}
	if strings.Contains(agentUnit, "CapabilityBoundingSet=\n") || strings.Contains(agentUnit, "AmbientCapabilities=CAP_") {
		t.Error("workspace supervisor capability boundary is not exact")
	}
	buildkitUnit := readArchitectureFile(t, filepath.Join(root, "infra", "images", "workspace", "ai-native-paas-buildkit.service"))
	for _, required := range []string{"User=workspace-task", "Group=workspace-task", "SupplementaryGroups=workspace-shared", "Environment=HOME=/home/workspace-task"} {
		if !strings.Contains(buildkitUnit, required) {
			t.Errorf("workspace BuildKit unit lacks %q", required)
		}
	}
	imageBuild := readArchitectureFile(t, filepath.Join(root, "scripts", "build-workspace-image.sh"))
	for _, required := range []string{
		"useradd --uid 1000 --gid workspace-agent", "useradd --uid 1001 --gid workspace-task",
		"useradd --uid 1002 --gid workspace-verified",
		"install -d -m 2750 -o root -g workspace-agent /var/lib/ai-native-paas/identity",
		"install -d -m 0770 -o workspace-task -g workspace-shared /workspace",
	} {
		if !strings.Contains(imageBuild, required) {
			t.Errorf("workspace image provisioning lacks %q", required)
		}
	}
}

func TestArchitecture_WorkspaceImageUsesCompactSignedQCOW2(t *testing.T) {
	root := repositoryRoot(t)
	imageBuild := readArchitectureFile(t, filepath.Join(root, "scripts", "build-workspace-image.sh"))
	workflow := readArchitectureFile(t, filepath.Join(root, ".github", "workflows", "workspace-agent.yml"))
	importer := readArchitectureFile(t, filepath.Join(root, "scripts", "import-timeweb-custom-image.py"))
	for _, required := range []string{
		`qemu-img convert -f qcow2 -O qcow2 -c`,
		`qemu-img check -f qcow2`,
		`ai-native-paas-workspace.qcow2`,
	} {
		if !strings.Contains(imageBuild, required) {
			t.Errorf("workspace image build lacks compact QCOW2 invariant %q", required)
		}
	}
	if strings.Contains(imageBuild, `-O raw`) || strings.Contains(imageBuild, `xz --threads`) {
		t.Error("workspace image build still expands the sparse disk into a staged RAW stream")
	}
	if !strings.Contains(workflow, `ai-native-paas-workspace.qcow2.sigstore.json`) || !strings.Contains(workflow, `ai-native-paas-workspace.qcow2`) {
		t.Error("workspace QCOW2 artifact is not signed in CI")
	}
	for _, required := range []string{`choices=("raw.xz", "qcow2")`, `upload_direct(`, `qcow2 staging key must end with .qcow2`} {
		if !strings.Contains(importer, required) {
			t.Errorf("workspace image importer lacks QCOW2 control %q", required)
		}
	}
}

func readArchitectureFile(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
