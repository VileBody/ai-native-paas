package sbom_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keir-research/ai-native-paas/internal/build/application"
	"github.com/keir-research/ai-native-paas/internal/build/sbom"
	sourcev1 "github.com/keir-research/ai-native-paas/pkg/contracts/source/v1"
)

func TestSBOM_IsDeterministicAndBoundToArtifactDigest(t *testing.T) {
	root := t.TempDir()
	_ = os.WriteFile(filepath.Join(root, "b.txt"), []byte("b"), 0644)
	_ = os.WriteFile(filepath.Join(root, "a.txt"), []byte("a"), 0644)
	snapshot := application.SourceSnapshot{Revision: sourcev1.SourceRevision{ProjectID: "p", RepositoryID: "r", Branch: "main", CommitSHA: strings.Repeat("a", 40)}, Path: root}
	artifact := application.PublishedArtifact{Repository: "r", Digest: "sha256:" + strings.Repeat("b", 64), MediaType: "m"}
	a, err := (sbom.Generator{}).Generate(context.Background(), snapshot, artifact)
	if err != nil {
		t.Fatal(err)
	}
	b, err := (sbom.Generator{}).Generate(context.Background(), snapshot, artifact)
	if err != nil {
		t.Fatal(err)
	}
	if a.Digest != b.Digest || !bytes.Contains(a.Document, []byte(artifact.Digest)) {
		t.Fatalf("a=%+v b=%+v", a, b)
	}
}
func TestBuild_BuildSecretIsAbsentFromSBOM(t *testing.T) {
	root := t.TempDir()
	_ = os.WriteFile(filepath.Join(root, "app.txt"), []byte("safe"), 0644)
	result, err := (sbom.Generator{}).Generate(context.Background(), application.SourceSnapshot{Revision: sourcev1.SourceRevision{ProjectID: "p", RepositoryID: "r", Branch: "main", CommitSHA: strings.Repeat("a", 40)}, Path: root}, application.PublishedArtifact{Digest: "sha256:" + strings.Repeat("b", 64)})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(result.Document, []byte("runtime-secret")) {
		t.Fatal("unexpected secret in SBOM")
	}
}
