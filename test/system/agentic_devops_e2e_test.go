//go:build system_e2e

package system_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const maxEvidenceBytes = 4 << 20

type systemEvidence struct {
	Schema     string          `json:"schema"`
	Scenario   string          `json:"scenario"`
	RunID      string          `json:"run_id"`
	Success    bool            `json:"success"`
	StartedAt  time.Time       `json:"started_at"`
	FinishedAt time.Time       `json:"finished_at"`
	Assertions map[string]bool `json:"assertions"`
	Artifacts  json.RawMessage `json:"artifacts,omitempty"`
}

func runSystemScenario(t *testing.T, scenario string, required ...string) systemEvidence {
	t.Helper()
	driver := strings.TrimSpace(os.Getenv("PAAS_E2E_DRIVER"))
	config := strings.TrimSpace(os.Getenv("PAAS_E2E_CONFIG"))
	if driver == "" || config == "" {
		t.Skip("PAAS_E2E_DRIVER and PAAS_E2E_CONFIG are required for live system gates")
	}
	absDriver, err := filepath.Abs(driver)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(absDriver)
	if err != nil || info.IsDir() || info.Mode()&0o111 == 0 {
		t.Fatalf("PAAS_E2E_DRIVER is not an executable file: %v", err)
	}
	absConfig, err := filepath.Abs(config)
	if err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(absConfig); err != nil || info.IsDir() || info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("PAAS_E2E_CONFIG must be a private regular file (mode *00): mode=%v err=%v", modeOf(info), err)
	}

	sentinelBytes := make([]byte, 24)
	if _, err := rand.Read(sentinelBytes); err != nil {
		t.Fatal(err)
	}
	sentinel := "PAAS_E2E_SECRET_" + hex.EncodeToString(sentinelBytes)
	timeout := 45 * time.Minute
	if raw := strings.TrimSpace(os.Getenv("PAAS_E2E_TIMEOUT")); raw != "" {
		parsed, err := time.ParseDuration(raw)
		if err != nil || parsed < time.Minute || parsed > 3*time.Hour {
			t.Fatalf("invalid PAAS_E2E_TIMEOUT %q", raw)
		}
		timeout = parsed
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	command := exec.CommandContext(ctx, absDriver, "--scenario", scenario, "--config", absConfig, "--json")
	command.Env = append(os.Environ(), "PAAS_E2E_SENTINEL="+sentinel)
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stderr, err := command.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	type readResult struct {
		raw []byte
		err error
	}
	stdoutResult := make(chan readResult, 1)
	stderrResult := make(chan readResult, 1)
	go func() {
		raw, err := io.ReadAll(io.LimitReader(stdout, maxEvidenceBytes+1))
		stdoutResult <- readResult{raw: raw, err: err}
	}()
	go func() {
		raw, err := io.ReadAll(io.LimitReader(stderr, maxEvidenceBytes+1))
		stderrResult <- readResult{raw: raw, err: err}
	}()
	waitErr := command.Wait()
	stdoutRead, stderrRead := <-stdoutResult, <-stderrResult
	stdoutRaw, stdoutErr := stdoutRead.raw, stdoutRead.err
	stderrRaw, stderrErr := stderrRead.raw, stderrRead.err
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		t.Fatalf("scenario %s exceeded %s", scenario, timeout)
	}
	if stdoutErr != nil || stderrErr != nil {
		t.Fatalf("read driver output: stdout=%v stderr=%v", stdoutErr, stderrErr)
	}
	if len(stdoutRaw) > maxEvidenceBytes || len(stderrRaw) > maxEvidenceBytes {
		t.Fatal("E2E driver output exceeded 4 MiB evidence limit")
	}
	combined := append(append([]byte(nil), stdoutRaw...), stderrRaw...)
	if bytes.Contains(combined, []byte(sentinel)) {
		t.Fatal("secret sentinel leaked into E2E evidence or driver diagnostics")
	}
	if waitErr != nil {
		t.Fatalf("scenario %s failed: %v; stderr=%s", scenario, waitErr, safeDiagnostic(stderrRaw))
	}
	var evidence systemEvidence
	decoder := json.NewDecoder(bytes.NewReader(stdoutRaw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&evidence); err != nil {
		t.Fatalf("decode scenario evidence: %v", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		t.Fatal("E2E driver emitted more than one JSON value")
	}
	if evidence.Schema != "ai-native-paas.io/e2e-evidence/v1" || evidence.Scenario != scenario ||
		strings.TrimSpace(evidence.RunID) == "" || !evidence.Success || evidence.StartedAt.IsZero() ||
		evidence.FinishedAt.Before(evidence.StartedAt) || evidence.FinishedAt.After(time.Now().Add(5*time.Minute)) {
		t.Fatalf("invalid scenario evidence envelope: %#v", evidence)
	}
	for _, assertion := range required {
		if !evidence.Assertions[assertion] {
			t.Errorf("scenario %s missing successful assertion %q", scenario, assertion)
		}
	}
	if t.Failed() {
		t.FailNow()
	}
	return evidence
}

func modeOf(info os.FileInfo) os.FileMode {
	if info == nil {
		return 0
	}
	return info.Mode()
}

func safeDiagnostic(raw []byte) string {
	value := strings.TrimSpace(string(raw))
	if len(value) > 2048 {
		value = value[:2048] + "…"
	}
	return value
}

func TestSystem_OneButtonDeploySimpleGoService(t *testing.T) {
	runSystemScenario(t, "E2E-1", "project.created", "repository.private", "workspace.private_only", "build.trust_chain_complete", "gitops.committed", "argocd.healthy", "http.probe_succeeded", "workspace.destroyed", "audit.complete")
}

func TestSystem_DeployTemporalPostgresQdrantAndOpenRouter(t *testing.T) {
	runSystemScenario(t, "E2E-2", "dependencies.ordered", "estimate.approved", "resources.exactly_once", "secrets.references_only", "workloads.healthy", "capability.usable", "usage.attributed")
}

func TestSystem_CustomUnknownServiceUsesGenericHelmPath(t *testing.T) {
	runSystemScenario(t, "E2E-3", "generic.no_control_plane_entity", "policy.passed", "gitops.committed", "runtime.healthy", "artifact.marked_custom")
}

func TestSystem_DestructivePlanCannotReusePreviousApproval(t *testing.T) {
	runSystemScenario(t, "E2E-4", "plan.hash_changed", "approval.reuse_denied", "apply.not_started", "new_summary.presented", "usage.not_created")
}

func TestSystem_LostGitWebhookProviderResponseAndArgoStatusRecover(t *testing.T) {
	runSystemScenario(t, "E2E-5", "git_webhook.recovered", "provider_response.reconciled", "argo_status.reconciled", "resources.no_duplicates", "operation.terminal_correct")
}

func TestSystem_WorkspaceCompromiseCannotReachControlPlaneOrOtherTenant(t *testing.T) {
	runSystemScenario(t, "E2E-6", "metadata.denied", "admin_vpc.denied", "runtime_vpc.denied", "foreign_workspace.denied", "provider_master_key.absent", "cluster_admin.absent", "workspace.destroyed", "incident.audited")
}

func TestSystem_CostBudgetStopsAutonomousRepairLoop(t *testing.T) {
	runSystemScenario(t, "E2E-7", "budget.exhausted", "task.paused", "new_costs.stopped", "state.preserved", "recommendation.presented")
}

func TestSystem_RuntimeDriftReconcilesFromGitNotWorkspaceMemory(t *testing.T) {
	runSystemScenario(t, "E2E-8", "workspace.absent", "drift.detected", "git.desired_state_authoritative", "runtime.reconciled", "imperative_mutation.absent")
}

func TestSystem_ProjectDeletionRetainsAndPurgesAccordingToExplicitPolicy(t *testing.T) {
	runSystemScenario(t, "E2E-9", "stateless.removed", "preview.removed", "database.retained", "credentials.revoked", "purge.requires_separate_approval")
}

func TestSystem_SecretSentinelAbsentAcrossAllSurfaces(t *testing.T) {
	runSystemScenario(t, "E2E-10", "secret.allowed_surfaces_only", "logs.clean", "git.clean", "state.clean", "postgres.clean", "nats.clean", "registry_metadata.clean", "audit.clean")
}

func TestSystem_SuspensionStopsMutationButPreservesRecoverability(t *testing.T) {
	runSystemScenario(t, "E2E-11", "suspension.mutation_denied", "source.preserved", "state.preserved", "data.preserved", "resume.reconciled", "identity.stable")
}

func TestSystem_BackupRestoreReconstructsControlAndProjectState(t *testing.T) {
	runSystemScenario(t, "E2E-12", "postgres.restored", "git.restored", "tofu_state.restored", "secret_metadata.restored", "registry.restored", "gitops.restored", "resources.no_duplicates", "audit_chain.complete")
}
