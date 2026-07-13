package localoci_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/keir-research/ai-native-paas/internal/build/application"
	"github.com/keir-research/ai-native-paas/internal/build/domain"
	"github.com/keir-research/ai-native-paas/internal/build/localoci"
	buildv1 "github.com/keir-research/ai-native-paas/pkg/contracts/build/v1"
	sourcev1 "github.com/keir-research/ai-native-paas/pkg/contracts/source/v1"
)

func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	return filepath.Clean(filepath.Join(filepath.Dir(file), "../../.."))
}
func fixture(t *testing.T, name string) string {
	t.Helper()
	return filepath.Join(repoRoot(t), "test", "fixtures", "build", name)
}
func request(t *testing.T, runtimeName, fixtureName string) application.BuildExecutionRequest {
	t.Helper()
	return application.BuildExecutionRequest{
		BuildID: "bld-" + runtimeName, TenantID: "t1", BuilderDigest: "sha256:" + strings.Repeat("b", 64), RunImageDigest: "sha256:" + strings.Repeat("c", 64),
		Source:    application.SourceSnapshot{Revision: sourcev1.SourceRevision{ProjectID: "p1", RepositoryID: "r1", Branch: "main", CommitSHA: strings.Repeat("a", 40)}, Path: fixture(t, fixtureName)},
		Detection: application.Detection{Runtime: runtimeName, Backend: application.BackendBuildpacks, BuildpackID: "fixture/" + runtimeName},
		Config:    domain.BuildConfig{}, Repository: "registry.test/tenants/t1/apps/p1",
	}
}
func TestLocalOCI_GoFixtureBuildsImmutableManifest(t *testing.T) {
	root := t.TempDir()
	output, err := (localoci.Builder{Root: root}).Build(context.Background(), request(t, "go", "hello-go"))
	if err != nil {
		t.Fatal(err)
	}
	if !buildv1.ValidDigest(output.ManifestDigest) {
		t.Fatalf("digest=%s", output.ManifestDigest)
	}
	actual, err := localoci.ManifestDigest(output.OCILayoutPath)
	if err != nil || actual != output.ManifestDigest {
		t.Fatalf("actual=%s err=%v", actual, err)
	}
}
func TestLocalOCI_NodeFixtureBuildsImmutableManifest(t *testing.T) {
	output, err := (localoci.Builder{Root: t.TempDir()}).Build(context.Background(), request(t, "nodejs", "hello-node"))
	if err != nil || !buildv1.ValidDigest(output.ManifestDigest) {
		t.Fatalf("output=%+v err=%v", output, err)
	}
}
func TestLocalOCI_PythonFixtureBuildsImmutableManifest(t *testing.T) {
	output, err := (localoci.Builder{Root: t.TempDir()}).Build(context.Background(), request(t, "python", "hello-python"))
	if err != nil || !buildv1.ValidDigest(output.ManifestDigest) {
		t.Fatalf("output=%+v err=%v", output, err)
	}
}
func TestBuild_SyntaxErrorIsUserFailure(t *testing.T) {
	_, err := (localoci.Builder{Root: t.TempDir()}).Build(context.Background(), request(t, "nodejs", "invalid-node"))
	if !domain.HasCode(err, domain.CodeUserFailure) {
		t.Fatalf("err=%v", err)
	}
}
func TestBuild_BuildSecretIsAbsentFromFinalImageEnvironment(t *testing.T) {
	req := request(t, "nodejs", "hello-node")
	req.Secrets = []application.BuildSecret{{Name: "npm-token", Value: "ultra-secret", AllowedPhases: []application.BuildPhase{application.PhaseBuild}}}
	req.Environment = map[string]string{"NODE_ENV": "production"}
	output, err := (localoci.Builder{Root: t.TempDir()}).Build(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if containsTree(t, output.OCILayoutPath, "ultra-secret") {
		t.Fatal("build secret leaked into OCI layout")
	}
	config := readConfig(t, output.OCILayoutPath)
	raw, _ := json.Marshal(config)
	if !bytes.Contains(raw, []byte("NODE_ENV=production")) {
		t.Fatalf("config=%s", raw)
	}
}
func TestLocalOCI_SameSourceProducesSameDigest(t *testing.T) {
	first, err := (localoci.Builder{Root: t.TempDir()}).Build(context.Background(), request(t, "nodejs", "hello-node"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := (localoci.Builder{Root: t.TempDir()}).Build(context.Background(), request(t, "nodejs", "hello-node"))
	if err != nil {
		t.Fatal(err)
	}
	if first.ManifestDigest != second.ManifestDigest {
		t.Fatalf("first=%s second=%s", first.ManifestDigest, second.ManifestDigest)
	}
}
func containsTree(t *testing.T, root, value string) bool {
	t.Helper()
	found := false
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if bytes.Contains(raw, []byte(value)) {
			found = true
		}
		return nil
	})
	return found
}
func readConfig(t *testing.T, layout string) map[string]any {
	t.Helper()
	indexRaw, _ := os.ReadFile(filepath.Join(layout, "index.json"))
	var index struct {
		Manifests []struct {
			Digest string `json:"digest"`
		} `json:"manifests"`
	}
	_ = json.Unmarshal(indexRaw, &index)
	manifestRaw, _ := os.ReadFile(filepath.Join(layout, "blobs", "sha256", strings.TrimPrefix(index.Manifests[0].Digest, "sha256:")))
	var manifest struct {
		Config struct {
			Digest string `json:"digest"`
		} `json:"config"`
	}
	_ = json.Unmarshal(manifestRaw, &manifest)
	configRaw, _ := os.ReadFile(filepath.Join(layout, "blobs", "sha256", strings.TrimPrefix(manifest.Config.Digest, "sha256:")))
	var config map[string]any
	_ = json.Unmarshal(configRaw, &config)
	return config
}

func TestLocalOCI_PythonBuildDoesNotMutateSourceTree(t *testing.T) {
	sourceDir := t.TempDir()
	for _, name := range []string{"main.py", "pyproject.toml"} {
		raw, err := os.ReadFile(filepath.Join(fixture(t, "hello-python"), name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(sourceDir, name), raw, 0644); err != nil {
			t.Fatal(err)
		}
	}
	before, err := os.ReadFile(filepath.Join(sourceDir, "main.py"))
	if err != nil {
		t.Fatal(err)
	}
	req := request(t, "python", "hello-python")
	req.Source.Path = sourceDir
	if _, err := (localoci.Builder{Root: t.TempDir()}).Build(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(filepath.Join(sourceDir, "main.py"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("python build mutated source file")
	}
	err = filepath.WalkDir(sourceDir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Name() == "__pycache__" || strings.HasSuffix(entry.Name(), ".pyc") {
			t.Errorf("python build created source-tree cache: %s", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
