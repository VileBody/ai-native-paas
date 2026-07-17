package contract_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}
func readManifest(t *testing.T, relative string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(repositoryRoot(t), filepath.FromSlash(relative)))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
func requireAll(t *testing.T, raw string, values ...string) {
	t.Helper()
	for _, value := range values {
		if !strings.Contains(raw, value) {
			t.Fatalf("manifest missing %q\n%s", value, raw)
		}
	}
}
func forbidAll(t *testing.T, raw string, values ...string) {
	t.Helper()
	for _, value := range values {
		if strings.Contains(raw, value) {
			t.Fatalf("manifest unexpectedly contains %q\n%s", value, raw)
		}
	}
}

func TestArgo_DoesNotManageGeneratedDeploymentReplicas(t *testing.T) {
	raw := readManifest(t, "deploy/argocd/runtime-applicationset.yaml")
	requireAll(t, raw, "paasapp.yaml", "kind: PaaSApp", "/status")
	forbidAll(t, raw, "kind: Deployment", "kind: HorizontalPodAutoscaler", "/spec/replicas")
}

func TestDelete_ArgoApplicationSetPreservesResourcesOnControllerRemoval(t *testing.T) {
	raw := readManifest(t, "deploy/argocd/runtime-applicationset.yaml")
	requireAll(t, raw, "preserveResourcesOnDeletion: true")
	forbidAll(t, raw, "preserveResourcesOnDeletion: false")
}

func TestArgoApplication_UsesRestrictedAppProject(t *testing.T) {
	appset := readManifest(t, "deploy/argocd/runtime-applicationset.yaml")
	project := readManifest(t, "deploy/argocd/runtime-appproject.yaml")
	requireAll(t, appset, "project: runtime-cell-a")
	requireAll(t, project, "kind: AppProject", "name: runtime-cell-a", "permitOnlyProjectScopedClusters: true")
	forbidAll(t, appset, "project: default")
}

func TestArgoApplication_AutoSyncPruneSelfHealEnabled(t *testing.T) {
	raw := readManifest(t, "deploy/argocd/runtime-applicationset.yaml")
	requireAll(t, raw, "automated:", "enabled: true", "prune: true", "selfHeal: true", "ServerSideApply=true", "RespectIgnoreDifferences=true")
}

func TestArgoApplication_SourceIsOnlyCellGitOpsRepository(t *testing.T) {
	const repo = "https://git.example.invalid/platform/runtime-cell-a.git"
	appset := readManifest(t, "deploy/argocd/runtime-applicationset.yaml")
	project := readManifest(t, "deploy/argocd/runtime-appproject.yaml")
	if strings.Count(appset, repo) != 2 {
		t.Fatalf("ApplicationSet must use the exact repository in generator and source: count=%d", strings.Count(appset, repo))
	}
	if strings.Count(project, repo) != 1 {
		t.Fatalf("AppProject sourceRepos is not exact: count=%d", strings.Count(project, repo))
	}
	forbidAll(t, project, "sourceRepos:\n    - '*'", "sourceRepos:\n  - '*'")
}

func TestArgoApplication_DestinationIsOnlyOwnedNamespace(t *testing.T) {
	appset := readManifest(t, "deploy/argocd/runtime-applicationset.yaml")
	project := readManifest(t, "deploy/argocd/runtime-appproject.yaml")
	requireAll(t, appset, "namespace: '{{.metadata.namespace}}'", "server: https://kubernetes.default.svc", "CreateNamespace=false")
	requireAll(t, project, "namespace: app-*", "server: https://kubernetes.default.svc")
	forbidAll(t, project, "namespace: '*'", "server: '*'")
}

func TestArgoAppProject_DeniesClusterScopedResources(t *testing.T) {
	raw := readManifest(t, "deploy/argocd/runtime-appproject.yaml")
	requireAll(t, raw, "clusterResourceWhitelist:", "kind: Namespace", "namespaceResourceWhitelist:", "group: platform.example.com", "kind: PaaSApp")
	forbidAll(t, raw, "kind: '*'", "kind: ClusterRole", "kind: ClusterRoleBinding", "kind: CustomResourceDefinition", "kind: DaemonSet", "kind: Node", "kind: StorageClass")
	if strings.Count(raw, "clusterResourceWhitelist:") != 1 || strings.Count(raw, "kind: Namespace") != 1 {
		t.Fatal("Namespace must be the sole cluster-scoped allow-list entry")
	}
}

func TestPaaSAppCRD_IsNamespacedAndHasStatusSubresource(t *testing.T) {
	raw := readManifest(t, "deploy/crds/platform.example.com_paasapps.yaml")
	requireAll(t, raw, "scope: Namespaced", "name: v1alpha1", "served: true", "storage: true", "subresources:", "status: {}", "additionalProperties: false", "^sha256:[0-9a-f]{64}$", "enum: [sandboxed, dedicated]")
}

func TestRuntimeOperatorRBAC_HasNoSecretsOrNamespaceMutation(t *testing.T) {
	raw := readManifest(t, "deploy/rbac/runtime-operator.yaml")
	requireAll(t, raw, "resources: [paasapps/status]", "resources: [deployments]", "resources: [services]", "resources: [horizontalpodautoscalers]", "resources: [jobs]", "resources: [httproutes]", "resources: [networkpolicies]")
	forbidAll(t, raw, "resources: [secrets]", "resources: [namespaces]", "resources: ['*']", "verbs: ['*']")
}

func TestRuntimeEnvoyGateway_UsesFixedPrivateNodePortsAndScopedRoutes(t *testing.T) {
	proxy := readManifest(t, "deploy/runtime/envoy-gateway/base/envoy-proxy.yaml")
	gateway := readManifest(t, "deploy/runtime/envoy-gateway/base/gateway.yaml")
	requireAll(t, proxy,
		"type: NodePort", "externalTrafficPolicy: Cluster",
		"nodePort: 30080", "nodePort: 30443", "replicas: 1",
		"minAvailable: 0", "ipFamily: IPv4",
	)
	requireAll(t, gateway,
		"kind: Gateway", "gatewayClassName: ai-native-paas-runtime",
		"protocol: HTTP", "protocol: HTTPS", "mode: Terminate",
		"name: runtime-wildcard-tls", "from: Selector",
		"ai-native-paas.io/runtime-project: \"true\"",
	)
	forbidAll(t, proxy+gateway, "type: LoadBalancer", "nodePort: 50000", "from: All", "kind: ReferenceGrant")
}

func TestRuntimeEnvoyGateway_ImagesAndChartAreImmutable(t *testing.T) {
	raw := readManifest(t, "deploy/runtime/envoy-gateway/images.lock.json")
	var lock struct {
		Chart struct {
			Version string `json:"version"`
			Digest  string `json:"digest"`
		} `json:"chart"`
		Images struct {
			Gateway string `json:"gateway"`
			Envoy   string `json:"envoy"`
		} `json:"images"`
	}
	if err := json.Unmarshal([]byte(raw), &lock); err != nil {
		t.Fatal(err)
	}
	if lock.Chart.Version != "v1.8.2" || !strings.HasPrefix(lock.Chart.Digest, "sha256:") {
		t.Fatalf("Envoy Gateway chart is not pinned: %#v", lock.Chart)
	}
	for name, image := range map[string]string{"gateway": lock.Images.Gateway, "envoy": lock.Images.Envoy} {
		if !strings.Contains(image, "@sha256:") || strings.Contains(image, ":latest") {
			t.Fatalf("%s image is mutable: %s", name, image)
		}
	}
	manifests := readManifest(t, "deploy/runtime/envoy-gateway/base/envoy-proxy.yaml") +
		readManifest(t, "deploy/runtime/envoy-gateway/values-smoke.yaml") +
		readManifest(t, "deploy/runtime/envoy-gateway/values-provider-gate.yaml")
	for _, image := range []string{lock.Images.Gateway, lock.Images.Envoy} {
		if !strings.Contains(manifests, image) {
			t.Errorf("locked image is not used by manifests: %s", image)
		}
	}
}

func TestRuntimeEnvoyGateway_ProviderGateRestoresHA(t *testing.T) {
	controller := readManifest(t, "deploy/runtime/envoy-gateway/values-provider-gate.yaml")
	proxy := readManifest(t, "deploy/runtime/envoy-gateway/provider-gate/runtime-proxy-ha-patch.yaml")
	for _, raw := range []string{controller, proxy} {
		requireAll(t, raw, "replicas: 2", "podAntiAffinity:", "requiredDuringSchedulingIgnoredDuringExecution:", "minAvailable: 1")
	}
	forbidAll(t, controller+proxy, "replicas: 3", "type: LoadBalancer")
}
