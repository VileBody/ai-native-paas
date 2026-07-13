package v1_test

import (
	"encoding/json"
	"testing"

	buildv1 "github.com/keir-research/ai-native-paas/pkg/contracts/build/v1"
)

func TestArtifactRef_ValidatesImmutableDigest(t *testing.T) {
	ref := buildv1.ArtifactRef{ArtifactID: "art-1", Repository: "registry.test/t1/app", Digest: "sha256:" + repeat("a", 64), MediaType: "application/vnd.oci.image.manifest.v1+json"}
	if err := ref.Validate(); err != nil {
		t.Fatal(err)
	}
	ref.Digest = "latest"
	if err := ref.Validate(); err == nil {
		t.Fatal("mutable tag unexpectedly accepted as digest")
	}
}

func TestArtifactRef_JSONContractUsesFrozenFieldNames(t *testing.T) {
	ref := buildv1.ArtifactRef{ArtifactID: "art-1", Repository: "r", Digest: "sha256:" + repeat("0", 64), MediaType: "m"}
	raw, err := json.Marshal(ref)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"artifact_id":"art-1","repository":"r","digest":"sha256:` + repeat("0", 64) + `","media_type":"m"}`
	if string(raw) != want {
		t.Fatalf("json=%s want=%s", raw, want)
	}
}

func repeat(v string, n int) string {
	out := ""
	for i := 0; i < n; i++ {
		out += v
	}
	return out
}
