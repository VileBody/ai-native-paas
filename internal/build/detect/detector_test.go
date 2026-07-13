package detect_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/keir-research/ai-native-paas/internal/build/application"
	"github.com/keir-research/ai-native-paas/internal/build/detect"
	"github.com/keir-research/ai-native-paas/internal/build/domain"
)

func fixture(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, body := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}
func TestDetector_GoModuleSelectsGoBuildpack(t *testing.T) {
	d, err := (detect.Detector{}).Detect(context.Background(), fixture(t, map[string]string{"go.mod": "module x"}), domain.BuildConfig{})
	if err != nil || d.Runtime != "go" || d.BuildpackID != "paketo-buildpacks/go" {
		t.Fatal(d, err)
	}
}
func TestDetector_PackageJSONSelectsNodeBuildpack(t *testing.T) {
	d, err := (detect.Detector{}).Detect(context.Background(), fixture(t, map[string]string{"package.json": "{}"}), domain.BuildConfig{})
	if err != nil || d.Runtime != "nodejs" {
		t.Fatal(d, err)
	}
}
func TestDetector_PyprojectSelectsPythonBuildpack(t *testing.T) {
	d, err := (detect.Detector{}).Detect(context.Background(), fixture(t, map[string]string{"pyproject.toml": "[project]"}), domain.BuildConfig{})
	if err != nil || d.Runtime != "python" {
		t.Fatal(d, err)
	}
}
func TestDetector_DockerfileUsesAdvancedBackend(t *testing.T) {
	d, err := (detect.Detector{}).Detect(context.Background(), fixture(t, map[string]string{"Dockerfile": "FROM scratch"}), domain.BuildConfig{Type: domain.BuildTypeDockerfile})
	if err != nil || d.Backend != application.BackendDockerfile {
		t.Fatal(d, err)
	}
}
func TestDetector_AmbiguousProjectRequiresExplicitConfig(t *testing.T) {
	_, err := (detect.Detector{}).Detect(context.Background(), fixture(t, map[string]string{"go.mod": "module x", "package.json": "{}"}), domain.BuildConfig{})
	if !domain.HasCode(err, domain.CodeUserFailure) {
		t.Fatalf("err=%v", err)
	}
}
func TestDetector_UnsupportedProjectReturnsStableUserError(t *testing.T) {
	_, err := (detect.Detector{}).Detect(context.Background(), fixture(t, map[string]string{"README.md": "x"}), domain.BuildConfig{})
	if !domain.HasCode(err, domain.CodeUserFailure) {
		t.Fatalf("err=%v", err)
	}
}
func TestDetector_SourceRootLimitsDetectionScope(t *testing.T) {
	root := fixture(t, map[string]string{"go.mod": "module root", "web/package.json": "{}"})
	d, err := (detect.Detector{}).Detect(context.Background(), root, domain.BuildConfig{SourceRoot: "web"})
	if err != nil || d.Runtime != "nodejs" {
		t.Fatal(d, err)
	}
}
