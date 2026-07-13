package contract_test

import (
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
