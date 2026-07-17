package architecture_test

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
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
	for _, stack := range []string{"admin", "network-foundation", "cozystack-lab", "workspace-images"} {
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

func TestArchitecture_WorkspaceLogsUseDedicatedProtectedAdminBucket(t *testing.T) {
	root := repositoryRoot(t)
	raw, err := os.ReadFile(filepath.Join(root, "infra", "stacks", "admin", "main.tf"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, required := range []string{`resource "twc_s3_bucket" "workspace_logs"`, `type        = "private"`, "prevent_destroy = true"} {
		if !strings.Contains(text, required) {
			t.Fatalf("admin workspace log bucket missing %q", required)
		}
	}
	for _, forbidden := range []string{"image_staging.access_key", "twc_s3_bucket.state.access_key"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("workspace logs reuse another security domain: %q", forbidden)
		}
	}
}

func TestArchitecture_PostgresIntegrationDatabaseIsIsolated(t *testing.T) {
	admin := readRepositoryFile(t, "infra", "stacks", "admin", "main.tf")
	runner := readRepositoryFile(t, "scripts", "run-postgres-gates-via-admin-cluster.sh")
	for _, required := range []string{
		`resource "twc_database_instance" "integration_test"`,
		`name        = "ai_native_paas_integration_test"`,
		`instance_id = twc_database_instance.integration_test.id`,
	} {
		if !strings.Contains(admin, required) {
			t.Errorf("admin PostgreSQL isolation control %q is missing", required)
		}
	}
	for _, required := range []string{
		`test_database="${PAAS_POSTGRES_TEST_DATABASE:-ai_native_paas_integration_test}"`,
		`refusing to run destructive integration tests against the production database`,
		`integration test database is missing; provision it through infra/stacks/admin`,
	} {
		if !strings.Contains(runner, required) {
			t.Errorf("PostgreSQL gate isolation control %q is missing", required)
		}
	}
}

func TestArchitecture_AdminAndUserInfrastructureHaveDisjointNetworks(t *testing.T) {
	admin := readRepositoryFile(t, "infra", "stacks", "admin", "main.tf")
	network := readRepositoryFile(t, "infra", "stacks", "network-foundation", "main.tf")
	cozystack := readRepositoryFile(t, "infra", "stacks", "cozystack-lab", "main.tf")
	workspaces := readRepositoryFile(t, "infra", "stacks", "workspace-images", "main.tf")
	if !strings.Contains(admin, "192.168.73.0/24") || strings.Contains(network, "192.168.73.0/24") {
		t.Fatal("admin VPC must remain solely owned by the admin stack")
	}
	for _, cidr := range []string{"192.168.74.0/24", "192.168.75.0/24"} {
		if !strings.Contains(network, cidr) {
			t.Errorf("network-foundation does not own expected CIDR %s", cidr)
		}
	}
	for name, body := range map[string]string{"cozystack": cozystack, "workspaces": workspaces} {
		for _, forbidden := range []string{`resource "twc_vpc"`, `resource "twc_floating_ip"`, `resource "twc_router"`} {
			if strings.Contains(body, forbidden) {
				t.Errorf("%s stack owns shared network resource %q", name, forbidden)
			}
		}
	}
}

func TestArchitecture_NetworkFoundationAdoptsOnlyReviewedIPv4(t *testing.T) {
	main := readRepositoryFile(t, "infra", "stacks", "network-foundation", "main.tf")
	for _, required := range []string{
		`id = "6c842a77-1f4a-436d-ac3e-f86fd1af9454"`,
		`id = "7af71678-a5b2-47bc-9b78-ceb99cd20780"`,
		`self.ip == "5.42.126.95"`,
		`self.ip == "72.56.234.22"`,
		`prevent_destroy = true`,
		`edge_enabled = var.network_mode == "live"`,
		`port        = 6443`,
	} {
		if !strings.Contains(main, required) {
			t.Errorf("network-foundation missing IPv4 safety invariant %q", required)
		}
	}
	if strings.Count(main, `resource "twc_floating_ip"`) != 2 {
		t.Fatal("network-foundation must manage exactly the two imported IPv4 reservations")
	}
	for _, forbidden := range []string{"5.42.106.8", `balancer_port = 50000`, `public_port = "50000"`, `floating_ip_id            =`} {
		if strings.Contains(main, forbidden) {
			t.Errorf("network-foundation references forbidden address/exposure %q", forbidden)
		}
	}
}

func TestArchitecture_TalosBootstrapDNATIsEphemeralScopedAndCreatesNoIPv4(t *testing.T) {
	networkMain := readRepositoryFile(t, "infra", "stacks", "network-foundation", "main.tf")
	networkVariables := readRepositoryFile(t, "infra", "stacks", "network-foundation", "variables.tf")
	cozystackMain := readRepositoryFile(t, "infra", "stacks", "cozystack-lab", "main.tf")
	cozystackVariables := readRepositoryFile(t, "infra", "stacks", "cozystack-lab", "variables.tf")
	for _, required := range []string{
		`resource "twc_router_dnat_rule" "talos_bootstrap"`,
		`local.edge_enabled && var.talos_bootstrap_dnat_enabled`,
		`cp-1 = { public_port = "50011"`,
		`cp-2 = { public_port = "50012"`,
		`cp-3 = { public_port = "50013"`,
		`public_ip   = twc_floating_ip.shared_egress.ip`,
		`local_port  = "50000"`,
		`resource "twc_router_dnat_rule" "talos_disk_repair_ssh"`,
		`local.edge_enabled && var.talos_disk_repair_ssh_enabled`,
		`cp-1 = { public_port = "22011"`,
		`cp-2 = { public_port = "22012"`,
		`cp-3 = { public_port = "22013"`,
		`local_port  = "22"`,
	} {
		if !strings.Contains(networkMain, required) {
			t.Errorf("network-foundation temporary Talos DNAT control %q is missing", required)
		}
	}
	for _, required := range []string{
		`variable "talos_bootstrap_dnat_enabled"`, `default     = false`,
		`can(regex("/32$", var.talos_bootstrap_source_cidr))`,
		`var.talos_bootstrap_source_cidr != "0.0.0.0/32"`,
		`variable "talos_disk_repair_ssh_enabled"`,
		`can(regex("/32$", var.talos_disk_repair_ssh_source_cidr))`,
		`var.talos_disk_repair_ssh_source_cidr != "0.0.0.0/32"`,
	} {
		if !strings.Contains(networkVariables, required) || !strings.Contains(cozystackVariables, required) {
			t.Errorf("both network and node states must enforce temporary DNAT control %q", required)
		}
	}
	for _, required := range []string{
		`resource "twc_firewall_rule" "node_talos_bootstrap_dnat"`,
		`local.cell_enabled && var.talos_bootstrap_dnat_enabled`,
		`port        = 50000`, `cidr        = var.talos_bootstrap_source_cidr`,
		`condition     = !var.talos_bootstrap_dnat_enabled`,
		`resource "twc_firewall_rule" "node_talos_disk_repair_ssh"`,
		`local.cell_enabled && var.talos_disk_repair_ssh_enabled`,
		`port        = 22`, `cidr        = var.talos_disk_repair_ssh_source_cidr`,
	} {
		if !strings.Contains(cozystackMain, required) {
			t.Errorf("Cozystack node bootstrap exception control %q is missing", required)
		}
	}
	if strings.Count(networkMain, `resource "twc_floating_ip"`) != 2 {
		t.Fatal("temporary bootstrap transport must not create another IPv4")
	}
}

func TestArchitecture_CozystackLabIsPinnedAndSizedForThreeNodes(t *testing.T) {
	main := readRepositoryFile(t, "infra", "stacks", "cozystack-lab", "main.tf")
	variables := readRepositoryFile(t, "infra", "stacks", "cozystack-lab", "variables.tf")
	outputs := readRepositoryFile(t, "infra", "stacks", "cozystack-lab", "outputs.tf")
	for _, node := range []string{`cp-1 = "192.168.74.11"`, `cp-2 = "192.168.74.12"`, `cp-3 = "192.168.74.13"`} {
		if !strings.Contains(main, node) {
			t.Errorf("Cozystack lab missing node declaration %q", node)
		}
	}
	for _, pinned := range []string{
		`var.runtime_edge_private_ip != ""`,
		`var.runtime_ingress_ip != ""`,
		`default     = "off"`,
		`default     = "v1.13.0"`,
		`default     = "v1.5.0"`,
		"@sha256:37caed57ac67316af15ecf55f05b04864527bed9f4b379abd681ea6ece9a64a4",
	} {
		if !strings.Contains(variables, pinned) {
			t.Errorf("Cozystack lab missing immutable sizing/release value %q", pinned)
		}
	}
	for _, profileValue := range []string{
		"preset_id         = 4803",
		"provider_gate_full = {",
		"configurator_id   = 31",
		"cpu               = 8",
		"ram_mb            = 24576",
		"system_disk_mb    = 81920",
		"data_disk_mb      = 40960",
		"data_disk_mb      = 266240",
		"network_mbps      = 1000",
		`mode = "no_nat"`,
		`cluster_endpoint   = "https://${var.runtime_edge_private_ip}:6443"`,
		"CREATE-3X-8VCPU-24GIB-COZYSTACK-PROVIDER-GATE-FULL",
		`"net.ipv6.conf.all.disable_ipv6"`,
	} {
		if !strings.Contains(main, profileValue) && !strings.Contains(variables, profileValue) {
			t.Errorf("Cozystack profile sizing/gate missing %q", profileValue)
		}
	}
	for _, forbidden := range []string{`resource "twc_floating_ip"`, "floating_ip_id", "main_ipv4"} {
		if strings.Contains(main, forbidden) {
			t.Errorf("Cozystack nodes are not private-only: found %q", forbidden)
		}
	}
	if !strings.Contains(outputs, "public_ipv4s    = 0") {
		t.Fatal("Cozystack compute state does not explicitly report zero public IPv4 addresses")
	}
	for _, control := range []string{`name = "none"`, "disabled = true", "allowSchedulingOnControlPlanes = true"} {
		if !strings.Contains(main, control) {
			t.Errorf("Cozystack Talos config missing %q", control)
		}
	}
}

func TestArchitecture_TalosBootstrapUsesDisposablePrivateRunner(t *testing.T) {
	main := readRepositoryFile(t, "infra", "stacks", "cozystack-lab", "main.tf")
	template := readRepositoryFile(t, "infra", "stacks", "cozystack-lab", "templates", "private-bootstrap-runner-cloud-init.yaml.tftpl")
	diskTemplate := readRepositoryFile(t, "infra", "stacks", "cozystack-lab", "templates", "talos-bootstrap-cloud-init.yaml.tftpl")
	for _, required := range []string{
		`resource "twc_server" "bootstrap_runner"`, `default     = 5943`,
		`ip   = "192.168.74.7"`, `mode = "no_nat"`, `is_root_password_required = false`,
		`direction   = "egress"`, `port        = 50000`, `cidr        = "192.168.74.0/24"`,
		`bootstrap_runner_enabled`, `runtime_router_id`,
	} {
		if !strings.Contains(main, required) && !strings.Contains(readRepositoryFile(t, "infra", "stacks", "cozystack-lab", "variables.tf"), required) {
			t.Errorf("private bootstrap runner control %q is missing", required)
		}
	}
	for _, required := range []string{
		"sha256sum --check --strict MANIFEST.sha256", "talosctl_sha256", "apply-config --insecure",
		"health --wait-timeout 15m", "openssl enc -aes-256-cbc", "--request PUT", "shutdown -h now",
	} {
		if !strings.Contains(template, required) {
			t.Errorf("bootstrap runner template control %q is missing", required)
		}
	}
	if !strings.Contains(diskTemplate, "mount -o remount,size=6G /dev/shm") {
		t.Error("smoke-node Talos disk writer does not expand /dev/shm for the pinned RAW image")
	}
	for _, required := range []string{
		`runtime_gateway_ip`, `ip route replace default via`, `resolvectl dns eth1 1.1.1.1`,
	} {
		if !strings.Contains(diskTemplate, required) && !strings.Contains(template, required) && !strings.Contains(main, required) {
			t.Errorf("private bootstrap networking control %q is missing", required)
		}
	}
	if strings.Contains(main, `cidr        = "192.168.74.1/32"`) {
		t.Error("bootstrap runner treats the private gateway as a DNS resolver")
	}
	for _, forbidden := range []string{
		`resource "talos_machine_configuration_apply"`, `resource "talos_machine_bootstrap"`,
		`resource "talos_cluster_kubeconfig"`, "floating_ip_id",
	} {
		if strings.Contains(main, forbidden) {
			t.Errorf("Talos bootstrap still depends on public/local execution: found %q", forbidden)
		}
	}
}

func TestArchitecture_CozystackPackagePolicyIsGoldenAndFailClosed(t *testing.T) {
	raw := readRepositoryFile(t, "infra", "stacks", "cozystack-lab", "packages", "profile-policy.json")
	sum := fmt.Sprintf("%x", sha256.Sum256([]byte(raw)))
	if sum != "0cdf942a83217cc2bdfe8e097e16e9f7726e2145ae1297287ab3847543ff46e0" {
		t.Fatalf("Cozystack package golden changed: got sha256 %s", sum)
	}

	var policy struct {
		CozystackVersion    string   `json:"cozystack_version"`
		DenyUnknownPackages bool     `json:"deny_unknown_packages"`
		RootPackages        []string `json:"root_packages"`
		Profiles            map[string]struct {
			AllowedPackages []string `json:"allowed_packages"`
		} `json:"profiles"`
		AlwaysForbidden []string `json:"always_forbidden"`
	}
	if err := json.Unmarshal([]byte(raw), &policy); err != nil {
		t.Fatal(err)
	}
	if policy.CozystackVersion != "v1.5.0" || !policy.DenyUnknownPackages {
		t.Fatal("Cozystack package policy must pin v1.5.0 and deny unknown packages")
	}
	if len(policy.RootPackages) != 1 || policy.RootPackages[0] != "cozystack.cozystack-platform" {
		t.Fatal("Cozystack package policy must explicitly approve the root platform Package")
	}
	if len(policy.Profiles["smoke"].AllowedPackages) != 18 ||
		len(policy.Profiles["provider_gate"].AllowedPackages) != 28 ||
		len(policy.Profiles["provider_gate_full"].AllowedPackages) != 40 {
		t.Fatalf(
			"unexpected profile package counts: smoke=%d provider_gate=%d provider_gate_full=%d",
			len(policy.Profiles["smoke"].AllowedPackages),
			len(policy.Profiles["provider_gate"].AllowedPackages),
			len(policy.Profiles["provider_gate_full"].AllowedPackages),
		)
	}
	for _, profile := range []string{"smoke", "provider_gate", "provider_gate_full"} {
		allowed := map[string]bool{}
		for _, name := range policy.Profiles[profile].AllowedPackages {
			allowed[name] = true
		}
		for _, forbidden := range policy.AlwaysForbidden {
			if allowed[forbidden] {
				t.Errorf("%s profile permits heavyweight package %s", profile, forbidden)
			}
		}
	}

	validator := readRepositoryFile(t, "scripts", "validate-cozystack-package-set.py")
	for _, required := range []string{"provider_gate_full", "missing = expected - actual", "unknown = actual - expected", "forbidden = actual.intersection", "forbidden_packages", "return 1"} {
		if !strings.Contains(validator, required) {
			t.Errorf("Cozystack package validator is not fail-closed: missing %q", required)
		}
	}
}

func TestArchitecture_CozystackPackageValidatorExecutesAgainstGolden(t *testing.T) {
	root := repositoryRoot(t)
	policyRaw, err := os.ReadFile(filepath.Join(root, "infra", "stacks", "cozystack-lab", "packages", "profile-policy.json"))
	if err != nil {
		t.Fatal(err)
	}
	var policy struct {
		Profiles map[string]struct {
			AllowedPackages []string `json:"allowed_packages"`
		} `json:"profiles"`
	}
	if err := json.Unmarshal(policyRaw, &policy); err != nil {
		t.Fatal(err)
	}

	render := func(packages []string) string {
		var manifest strings.Builder
		for index, name := range packages {
			if index > 0 {
				manifest.WriteString("---\n")
			}
			fmt.Fprintf(&manifest, "apiVersion: source.toolkit.fluxcd.io/v1\nkind: Package\nmetadata:\n  name: %s\n", name)
		}
		return manifest.String()
	}
	manifest := filepath.Join(t.TempDir(), "packages.yaml")
	if err := os.WriteFile(manifest, []byte(render(policy.Profiles["smoke"].AllowedPackages)), 0o600); err != nil {
		t.Fatal(err)
	}
	validator := filepath.Join(root, "scripts", "validate-cozystack-package-set.py")
	command := exec.Command("python3", validator, "--profile", "smoke", "--manifest", manifest)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("golden package set rejected: %v\n%s", err, output)
	}

	if err := os.WriteFile(manifest, []byte(render(policy.Profiles["provider_gate_full"].AllowedPackages)), 0o600); err != nil {
		t.Fatal(err)
	}
	command = exec.Command("python3", validator, "--profile", "provider_gate_full", "--manifest", manifest)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("full provider gate package set rejected: %v\n%s", err, output)
	}

	withProfileForbidden := append(append([]string(nil), policy.Profiles["provider_gate"].AllowedPackages...), "cozystack.monitoring-application")
	if err := os.WriteFile(manifest, []byte(render(withProfileForbidden)), 0o600); err != nil {
		t.Fatal(err)
	}
	command = exec.Command("python3", validator, "--profile", "provider_gate", "--manifest", manifest)
	output, err := command.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "cozystack.monitoring-application") {
		t.Fatalf("profile-specific forbidden package was not rejected: err=%v output=%s", err, output)
	}

	withUnknown := append(append([]string(nil), policy.Profiles["smoke"].AllowedPackages...), "cozystack.unreviewed-heavy-package")
	if err := os.WriteFile(manifest, []byte(render(withUnknown)), 0o600); err != nil {
		t.Fatal(err)
	}
	command = exec.Command("python3", validator, "--profile", "smoke", "--manifest", manifest)
	output, err = command.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "cozystack.unreviewed-heavy-package") {
		t.Fatalf("unknown package was not rejected: err=%v output=%s", err, output)
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

func TestArchitecture_AdminCapacityModesRequireSnapshotsAndRestoreHA(t *testing.T) {
	main := readRepositoryFile(t, "infra", "stacks", "admin", "main.tf")
	variables := readRepositoryFile(t, "infra", "stacks", "admin", "variables.tf")
	for _, required := range []string{
		`contains(["off", "dev", "ha"], var.admin_capacity_mode)`,
		`system_worker_count   = var.admin_capacity_mode == "ha" ? 3 : var.admin_capacity_mode == "dev" ? 1 : 0`,
		`system_worker_enabled = local.system_worker_count > 0`,
		`count = local.system_worker_enabled ? 1 : 0`,
	} {
		if !strings.Contains(main+variables, required) {
			t.Errorf("admin capacity profile missing %q", required)
		}
	}
	checks := map[string][]string{
		"scripts/deploy-openbao.sh":        {"OPENBAO-RAFT-SNAPSHOT-VERIFIED", "ADMIN_CAPACITY_MODE", "values-dev.yaml", "values-off.yaml", "mutatingwebhookconfiguration"},
		"scripts/deploy-nats.sh":           {"NATS-JETSTREAM-BACKUP-VERIFIED", "ADMIN_CAPACITY_MODE", "values-dev.yaml", "values-off.yaml"},
		"scripts/reconcile-timeweb-csi.sh": {"ADMIN_CAPACITY_MODE", `"ai-native-paas.io/pool":"ci"`, `"maxUnavailable":1`, `"maxSurge":0`},
	}
	for path, required := range checks {
		body := readRepositoryFile(t, strings.Split(path, "/")...)
		for _, token := range required {
			if !strings.Contains(body, token) {
				t.Errorf("%s missing capacity safety control %q", path, token)
			}
		}
	}
	stateService := readRepositoryFile(t, "deploy", "admin", "state-service", "state-service.yaml")
	if !strings.Contains(stateService, "preferredDuringSchedulingIgnoredDuringExecution") {
		t.Error("state service must remain schedulable on the CI worker while system capacity is off")
	}
	openBaoOff := readRepositoryFile(t, "deploy", "admin", "openbao", "values-off.yaml")
	if !strings.Contains(openBaoOff, "enabled: false") || strings.Contains(openBaoOff, "injector:\n  replicas: 0") {
		t.Error("OpenBao off mode must remove the failure-closed injector webhook instead of leaving a zero-replica webhook backend")
	}
}

func TestArchitecture_CozystackCoreDNSDomainIsDurablyReconciled(t *testing.T) {
	cozystack := readRepositoryFile(t, "infra", "stacks", "cozystack-lab", "main.tf")
	reconcile := readRepositoryFile(t, "scripts", "reconcile-runtime-coredns-domain.sh")
	for _, required := range []string{
		`dnsDomain      = "cozy.local"`,
		`podSubnets     = ["10.244.0.0/16"]`,
		`serviceSubnets = ["10.96.0.0/16"]`,
	} {
		if !strings.Contains(cozystack, required) {
			t.Errorf("Talos cluster DNS invariant missing %q", required)
		}
	}
	for _, required := range []string{
		`RUNTIME_DNS_DOMAIN:-cozy.local`,
		`configmap coredns`,
		`kubernetes ${domain}`,
		`rollout restart deployment/coredns`,
		`jsonpath={.data.Corefile}`,
	} {
		if !strings.Contains(reconcile, required) {
			t.Errorf("CoreDNS reconcile script missing %q", required)
		}
	}
}
