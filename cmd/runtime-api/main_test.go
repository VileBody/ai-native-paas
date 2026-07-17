package main

import (
	"testing"

	"github.com/keir-research/ai-native-paas/internal/runtime/simulator"
	runtimev1 "github.com/keir-research/ai-native-paas/pkg/contracts/runtime/v1"
)

func TestDevelopmentRuntimeCellDefaultsToSimulator(t *testing.T) {
	for _, name := range []string{
		"RUNTIME_CELL_ID",
		"RUNTIME_REGION",
		"RUNTIME_GITOPS_REPOSITORY",
		"RUNTIME_CLUSTER_SERVER",
		"RUNTIME_ARGO_PROJECT",
		"RUNTIME_INGRESS_DOMAIN",
	} {
		t.Setenv(name, "")
	}

	req := developmentRuntimeCellRequest()
	if req.ID != simulator.CellID ||
		req.Region != simulator.Region ||
		req.CapacityUnits != simulator.CapacityUnits ||
		req.GitOpsRepository != simulator.GitOpsRepository ||
		req.ClusterServer != simulator.ClusterServer ||
		req.ArgoProject != simulator.ArgoProject ||
		req.IngressDomain != simulator.IngressDomain {
		t.Fatalf("development runtime cell is not simulator-backed: %#v", req)
	}
	if len(req.Isolation) != 1 || req.Isolation[0] != runtimev1.IsolationSandboxed {
		t.Fatalf("unexpected simulator isolation: %#v", req.Isolation)
	}
}

func TestDevelopmentRuntimeCellAllowsExplicitOverride(t *testing.T) {
	t.Setenv("RUNTIME_CELL_ID", "custom-sim")
	t.Setenv("RUNTIME_REGION", "dev")
	t.Setenv("RUNTIME_GITOPS_REPOSITORY", "https://git.example.invalid/custom/runtime.git")
	t.Setenv("RUNTIME_CLUSTER_SERVER", "https://kubernetes.default.svc")
	t.Setenv("RUNTIME_ARGO_PROJECT", "custom-sim")
	t.Setenv("RUNTIME_INGRESS_DOMAIN", "custom.runtime.internal")

	req := developmentRuntimeCellRequest()
	if req.ID != "custom-sim" || req.Region != "dev" || req.ArgoProject != "custom-sim" || req.IngressDomain != "custom.runtime.internal" {
		t.Fatalf("development runtime cell override ignored: %#v", req)
	}
}
