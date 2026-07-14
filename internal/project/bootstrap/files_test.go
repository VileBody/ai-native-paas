package bootstrap

import (
	"testing"

	projectv2 "github.com/keir-research/ai-native-paas/pkg/contracts/project/v2"
)

func TestBootstrapFiles_ContainStrictPlatformContractAndCanonicalRoots(t *testing.T) {
	digest := "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	files, err := Files(Options{Name: "booking", WorkspaceImageDigest: digest})
	if err != nil {
		t.Fatal(err)
	}
	byPath := make(map[string][]byte, len(files))
	for _, file := range files {
		if _, exists := byPath[file.Path]; exists {
			t.Fatalf("duplicate path %s", file.Path)
		}
		byPath[file.Path] = file.Content
	}
	contract, err := projectv2.Parse(byPath["platform.yaml"])
	if err != nil || contract.Metadata.Name != "booking" || contract.Workspace.ImageDigest != digest {
		t.Fatalf("contract=%#v err=%v", contract, err)
	}
	for _, path := range []string{"README.md", "infrastructure/tofu/main.tf", "deploy/base/kustomization.yaml", "deploy/environments/development/kustomization.yaml", "deploy/environments/production/kustomization.yaml", "recipes.lock.yaml"} {
		if len(byPath[path]) == 0 {
			t.Fatalf("missing bootstrap file %s", path)
		}
	}
}
