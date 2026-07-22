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

func TestArchitecture_WorkspaceEgressHasNoDirectInternetFallback(t *testing.T) {
	root := repositoryRoot(t)
	providerRaw, err := os.ReadFile(filepath.Join(root, "internal", "workspace", "timeweb", "provider.go"))
	if err != nil {
		t.Fatal(err)
	}
	provider := string(providerRaw)
	for _, required := range []string{"ControlPlanePort", "strconv.Itoa(p.controlPlanePort)", "EgressGatewayPort", "strconv.Itoa(p.egressGatewayPort)", `Port: "53"`} {
		if !strings.Contains(provider, required) {
			t.Fatalf("workspace firewall is missing %q", required)
		}
	}
	if strings.Contains(provider, `Port: "1-65535"`) {
		t.Fatal("workspace firewall permits every gateway port")
	}
	for _, filename := range []string{"ai-native-paas-workspace-agent.service", "ai-native-paas-buildkit.service"} {
		raw, err := os.ReadFile(filepath.Join(root, "infra", "images", "workspace", filename))
		if err != nil {
			t.Fatal(err)
		}
		if filename == "ai-native-paas-buildkit.service" && (!strings.Contains(string(raw), "HTTPS_PROXY=http://127.0.0.1:18081") || !strings.Contains(string(raw), "After=network-online.target ai-native-paas-workspace-agent.service")) {
			t.Fatal("rootless BuildKit is not bound to the workspace local proxy")
		}
	}
}
