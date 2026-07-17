package contract_test

import (
	"strings"
	"testing"
)

func TestRuntimeSimulator_IsExplicitlyDevelopmentOnly(t *testing.T) {
	raw := readManifest(t, "deploy/runtime/simulator/namespace.yaml") +
		readManifest(t, "deploy/runtime/simulator/runtime-cell-config.yaml") +
		readManifest(t, "deploy/runtime/simulator/quota.yaml") +
		readManifest(t, "deploy/runtime/simulator/networkpolicy.yaml") +
		readManifest(t, "deploy/runtime/simulator/argocd-appproject.yaml")

	requireAll(t, raw,
		"runtime_sim_k8s",
		"ai-native-paas.io/evidence-class: dev-product",
		"ai-native-paas.io/not-cozystack-live: \"true\"",
		"cozystack_live: \"false\"",
		"Development-only runtime simulator",
	)
	forbidAll(t, raw,
		"cozystack_live: \"true\"",
		"apps.cozystack.io",
		"cozy-system",
		"Talos",
		"LINSTOR",
	)
}

func TestRuntimeSimulator_IsBoundedInsideAdminClusterNamespace(t *testing.T) {
	raw := readManifest(t, "deploy/runtime/simulator/namespace.yaml") +
		readManifest(t, "deploy/runtime/simulator/quota.yaml") +
		readManifest(t, "deploy/runtime/simulator/networkpolicy.yaml")

	requireAll(t, raw,
		"name: paas-runtime-sim",
		"pod-security.kubernetes.io/enforce: restricted",
		"services.loadbalancers: \"0\"",
		"services.nodeports: \"0\"",
		"requests.cpu: \"2\"",
		"requests.storage: 40Gi",
		"kind: NetworkPolicy",
		"name: runtime-sim-default-deny",
	)
	forbidAll(t, raw,
		"type: LoadBalancer",
		"type: NodePort",
		"hostNetwork: true",
		"privileged: true",
		"kind: Secret",
		"stringData:",
	)
}

func TestRuntimeSimulator_ArgoProjectCannotEscapeSimulatorNamespace(t *testing.T) {
	raw := readManifest(t, "deploy/runtime/simulator/argocd-appproject.yaml")
	requireAll(t, raw,
		"kind: AppProject",
		"name: runtime-sim-k8s",
		"namespace: argocd",
		"sourceRepos:",
		"- https://git.example.invalid/platform/runtime-sim.git",
		"namespace: paas-runtime-sim",
		"clusterResourceWhitelist: []",
	)
	forbidAll(t, raw, "namespace: '*'", "server: '*'", "sourceRepos:\n    - '*'", "kind: Namespace")
	if strings.Count(raw, "namespace: paas-runtime-sim") != 1 {
		t.Fatalf("runtime simulator AppProject must target only paas-runtime-sim:\n%s", raw)
	}
}

func TestReadinessSeparatesSimulatorFromCozystackLiveGates(t *testing.T) {
	status := readManifest(t, "verification/status.yaml")
	readiness := readManifest(t, "verification/BETA_READINESS.md")
	adr := readManifest(t, "docs/adr/0007-runtime-simulator-vs-cozystack-live.md")

	requireAll(t, status, "DEV_PRODUCT_GREEN", "COZYSTACK_LIVE_GREEN", "PROVIDER_FULL_GREEN")
	requireAll(t, readiness, "DEV_PRODUCT_GREEN", "COZYSTACK_LIVE_GREEN", "PROVIDER_FULL_GREEN")
	requireAll(t, adr, "runtime_sim_k8s", "cozystack_live", "Simulator evidence", "may not close")
}
