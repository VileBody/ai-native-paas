package gitops

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/keir-research/ai-native-paas/internal/runtime/domain"
)

func TestPinnedHelmChartDigestRejectsTamperingAndLiveInputs(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "templates"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "Chart.yaml"), []byte("apiVersion: v2\nname: app\nversion: 1.0.0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	templatePath := filepath.Join(root, "templates", "config.yaml")
	stable := "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: app\n  namespace: project\ndata:\n  environment: {{ .Values.env | quote }}\n"
	if err := os.WriteFile(templatePath, []byte(stable), 0o600); err != nil {
		t.Fatal(err)
	}
	digest, err := PinnedHelmChartDigest(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyPinnedHelmChart(root, "1.0.0", digest); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(templatePath, []byte(stable+"# changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := VerifyPinnedHelmChart(root, "1.0.0", digest); !domain.HasCode(err, domain.CodeConflict) {
		t.Fatalf("tampered chart retained digest pin: %v", err)
	}
	if err := os.WriteFile(templatePath, []byte(stable+"# {{ env \"HOME\" }}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := PinnedHelmChartDigest(root); !domain.HasCode(err, domain.CodeForbidden) {
		t.Fatalf("host-environment template input accepted: %v", err)
	}
}
