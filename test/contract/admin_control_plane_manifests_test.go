package contract_test

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestAdminProjectAPI_UsesImmutableProductionImageAndSystemPool(t *testing.T) {
	raw := readManifest(t, "deploy/admin/control-plane/project-api.yaml")
	requireAll(t, raw,
		"PLATFORM_PROFILE", "value: production",
		"ai-native-paas.io/pool", "values: [system]",
		"ai-native-paas.io/system", "effect: NoSchedule",
		"runAsNonRoot: true", "readOnlyRootFilesystem: true", "drop: [ALL]",
		"type: ClusterIP", "secretName: project-api-secrets", "defaultMode: 0440",
		"ai-native-paas-project-api@sha256:",
	)
	forbidAll(t, raw, ":latest", "kind: Ingress", "type: LoadBalancer", "kind: Secret", "stringData:")
}

func TestAdminProjectAPI_ImageLockMatchesManifest(t *testing.T) {
	manifest := readManifest(t, "deploy/admin/control-plane/project-api.yaml")
	raw := readManifest(t, "deploy/admin/control-plane/images.lock.json")
	var lock struct {
		ProjectAPI struct {
			Image      string `json:"image"`
			Provenance string `json:"provenance"`
			SBOM       bool   `json:"sbom"`
		} `json:"project_api"`
	}
	if err := json.Unmarshal([]byte(raw), &lock); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(manifest, "image: "+lock.ProjectAPI.Image) || lock.ProjectAPI.Provenance != "mode=max" || !lock.ProjectAPI.SBOM {
		t.Fatalf("project API image evidence is inconsistent: %#v", lock.ProjectAPI)
	}
}

func TestAdminWorkspaceEgressGateway_IsMTLSOnlyAndFailClosed(t *testing.T) {
	raw := readManifest(t, "deploy/admin/workspace-egress-gateway/workspace-egress-gateway.yaml")
	requireAll(t, raw,
		"PLATFORM_PROFILE", "value: production", "WORKSPACE_EGRESS_CLIENT_CA_FILE",
		"WORKSPACE_EGRESS_ALLOWED_HOSTS", "WORKSPACE_EGRESS_DENIED_CIDRS",
		"ai-native-paas.io/pool", "values: [system]", "type: LoadBalancer",
		"port: 8443", "cidr: 0.0.0.0/0", "192.168.0.0/16",
		"runAsNonRoot: true", "readOnlyRootFilesystem: true", "drop: [ALL]",
		"ai-native-paas-workspace-egress-gateway@sha256:",
	)
	forbidAll(t, raw, ":latest", "automountServiceAccountToken: true", "kind: Ingress", "stringData:", "port: 80")
}

func TestAdminWorkspaceEgressGateway_ImageLockMatchesManifest(t *testing.T) {
	manifest := readManifest(t, "deploy/admin/workspace-egress-gateway/workspace-egress-gateway.yaml")
	raw := readManifest(t, "deploy/admin/workspace-egress-gateway/images.lock.json")
	var lock struct {
		Gateway struct {
			Image      string `json:"image"`
			Provenance string `json:"provenance"`
			SBOM       bool   `json:"sbom"`
		} `json:"workspace_egress_gateway"`
	}
	if err := json.Unmarshal([]byte(raw), &lock); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(manifest, "image: "+lock.Gateway.Image) || lock.Gateway.Provenance != "mode=max" || !lock.Gateway.SBOM {
		t.Fatalf("workspace egress gateway image evidence is inconsistent: %#v", lock.Gateway)
	}
}
