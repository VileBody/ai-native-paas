package architecture_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func readRepositoryFile(t *testing.T, path ...string) string {
	t.Helper()
	parts := append([]string{repositoryRoot(t)}, path...)
	raw, err := os.ReadFile(filepath.Join(parts...))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestArchitecture_OpenTofuStatesHaveUniqueBlobKeysAndEncryptedHTTPPlans(t *testing.T) {
	keys := map[string]bool{}
	for _, stack := range []string{"admin", "cozystack-lab", "workspace-images"} {
		backend := readRepositoryFile(t, "infra", "backend", stack+".s3.tfbackend.example")
		match := regexp.MustCompile(`(?m)^key\s*=\s*"([^"]+)"`).FindStringSubmatch(backend)
		if len(match) != 2 {
			t.Fatalf("%s backend has no state key", stack)
		}
		if keys[match[1]] {
			t.Fatalf("duplicate OpenTofu state key %q", match[1])
		}
		keys[match[1]] = true
		for _, required := range []string{"use_lockfile", "use_path_style", "s3.twcstorage.ru"} {
			if !strings.Contains(backend, required) {
				t.Errorf("%s backend missing %q", stack, required)
			}
		}

		versions := readRepositoryFile(t, "infra", "stacks", stack, "versions.tf")
		for _, required := range []string{`backend "http"`, `method "aes_gcm" "state"`, "plan {", "enforced = true"} {
			if !strings.Contains(versions, required) {
				t.Errorf("%s stack missing encrypted-state control %q", stack, required)
			}
		}
	}
}

func TestArchitecture_StateBootstrapIsAlsoEncryptedAndRemote(t *testing.T) {
	versions := readRepositoryFile(t, "infra", "bootstrap", "timeweb-state", "versions.tf")
	for _, required := range []string{`backend "http"`, `method "aes_gcm" "state"`, "plan {", "enforced = true"} {
		if !strings.Contains(versions, required) {
			t.Errorf("state bootstrap missing encrypted remote-state control %q", required)
		}
	}
	backendGenerator := readRepositoryFile(t, "scripts", "configure-http-state-backend.py")
	if !strings.Contains(backendGenerator, `"state-bootstrap"`) {
		t.Error("HTTP backend generator does not isolate the state-bootstrap namespace")
	}
}

func TestArchitecture_AdminAndUserInfrastructureHaveDisjointNetworks(t *testing.T) {
	files := map[string]string{
		"admin":      readRepositoryFile(t, "infra", "stacks", "admin", "main.tf"),
		"cozystack":  readRepositoryFile(t, "infra", "stacks", "cozystack-lab", "main.tf"),
		"workspaces": readRepositoryFile(t, "infra", "stacks", "workspace-images", "main.tf"),
	}
	expected := map[string]string{
		"admin":      "192.168.73.0/24",
		"cozystack":  "192.168.74.0/24",
		"workspaces": "192.168.75.0/24",
	}
	for owner, cidr := range expected {
		for other, body := range files {
			contains := strings.Contains(body, cidr)
			if owner == other && !contains {
				t.Errorf("%s stack does not own expected CIDR %s", owner, cidr)
			}
			if owner != other && contains {
				t.Errorf("%s CIDR %s leaked into %s stack", owner, cidr, other)
			}
		}
	}
}

func TestArchitecture_CozystackLabIsPinnedAndSizedForThreeNodes(t *testing.T) {
	main := readRepositoryFile(t, "infra", "stacks", "cozystack-lab", "main.tf")
	variables := readRepositoryFile(t, "infra", "stacks", "cozystack-lab", "variables.tf")
	for _, node := range []string{`cp-1 = "192.168.74.11"`, `cp-2 = "192.168.74.12"`, `cp-3 = "192.168.74.13"`} {
		if !strings.Contains(main, node) {
			t.Errorf("Cozystack lab missing node declaration %q", node)
		}
	}
	for _, pinned := range []string{
		`default     = 6633`,
		`default     = 266240`,
		`default     = "v1.13.0"`,
		`default     = "v1.5.0"`,
		"@sha256:37caed57ac67316af15ecf55f05b04864527bed9f4b379abd681ea6ece9a64a4",
	} {
		if !strings.Contains(variables, pinned) {
			t.Errorf("Cozystack lab missing immutable sizing/release value %q", pinned)
		}
	}
	for _, control := range []string{`name = "none"`, "disabled = true", "allowSchedulingOnControlPlanes = true"} {
		if !strings.Contains(main, control) {
			t.Errorf("Cozystack Talos config missing %q", control)
		}
	}
}

func TestArchitecture_ImageLockMatchesCozystackStack(t *testing.T) {
	raw := readRepositoryFile(t, "infra", "stacks", "workspace-images", "images.lock.json")
	var lock struct {
		CozystackTalos struct {
			CozystackVersion string `json:"cozystack_version"`
			TalosVersion     string `json:"talos_version"`
			SHA256           string `json:"sha256"`
			RawSHA256        string `json:"raw_sha256"`
			RawSizeBytes     int64  `json:"raw_size_bytes"`
			MoscowImport     string `json:"timeweb_moscow_import"`
			Installer        string `json:"installer"`
		} `json:"cozystack_talos"`
	}
	if err := json.Unmarshal([]byte(raw), &lock); err != nil {
		t.Fatal(err)
	}
	if lock.CozystackTalos.CozystackVersion != "v1.5.0" || lock.CozystackTalos.TalosVersion != "v1.13.0" {
		t.Fatalf("unexpected Cozystack/Talos release lock: %+v", lock.CozystackTalos)
	}
	if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(lock.CozystackTalos.SHA256) {
		t.Fatal("Talos boot artifact is not sha256 pinned")
	}
	if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(lock.CozystackTalos.RawSHA256) || lock.CozystackTalos.RawSizeBytes != 4453302272 {
		t.Fatal("decompressed Talos boot artifact identity is not pinned")
	}
	if lock.CozystackTalos.MoscowImport != "UNSUPPORTED_USE_VERIFIED_IN_PLACE_BOOTSTRAP" {
		t.Fatal("Timeweb Moscow custom-image fallback is not explicit")
	}
	if !regexp.MustCompile(`@sha256:[0-9a-f]{64}$`).MatchString(lock.CozystackTalos.Installer) {
		t.Fatal("Talos installer is not digest pinned")
	}
}

func TestArchitecture_AdminStateAdoptionDoesNotRotateDatabaseCredential(t *testing.T) {
	main := readRepositoryFile(t, "infra", "stacks", "admin", "main.tf")
	for _, required := range []string{
		"from = twc_k8s_node_group.platform",
		"to   = twc_k8s_node_group.ci",
		"length      = 16",
		"min_lower   = 4",
		"min_upper   = 2",
		"min_numeric = 4",
		`rotation = "2026-07-14-operator-output-containment-1"`,
	} {
		if !strings.Contains(main, required) {
			t.Errorf("admin state-adoption safety invariant missing %q", required)
		}
	}
}
