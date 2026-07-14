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
