package gitops

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGitOps_KustomizeRenderIsDeterministicAndPathSafe(t *testing.T) {
	root := t.TempDir()
	base := filepath.Join(root, "deploy", "environments", "production")
	if err := os.MkdirAll(base, 0750); err != nil {
		t.Fatal(err)
	}
	kustomization := "apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\nresources:\n  - deployment.yaml\n  - service.yaml\n"
	deployment := "apiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: app\nspec:\n  replicas: 1\n"
	service := "apiVersion: v1\nkind: Service\nmetadata:\n  name: app\nspec:\n  ports:\n    - port: 80\n"
	for name, raw := range map[string]string{"kustomization.yaml": kustomization, "deployment.yaml": deployment, "service.yaml": service} {
		if err := os.WriteFile(filepath.Join(base, name), []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
	}
	first, err := RenderKustomize(root, "deploy/environments/production")
	if err != nil {
		t.Fatal(err)
	}
	second, err := RenderKustomize(root, "deploy/environments/production")
	if err != nil || first.Digest != second.Digest || !bytes.Equal(first.Manifest, second.Manifest) || len(first.Resources) != 2 {
		t.Fatalf("first=%+v second=%+v err=%v", first, second, err)
	}
	for _, unsafe := range []string{"../production", "deploy/../production", "/tmp/production", "deploy\\production"} {
		if _, err := RenderKustomize(root, unsafe); err == nil {
			t.Fatalf("unsafe path accepted: %q", unsafe)
		}
	}
	for _, unsafe := range []string{
		"https://github.com/example/base//deployment.yaml?ref=main",
		"git@github.com:example/base/deployment.yaml",
	} {
		remote := strings.Replace(kustomization, "deployment.yaml", unsafe, 1)
		if err := os.WriteFile(filepath.Join(base, "kustomization.yaml"), []byte(remote), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := RenderKustomize(root, "deploy/environments/production"); err == nil {
			t.Fatalf("remote resource accepted: %q", unsafe)
		}
	}
	if err := os.WriteFile(filepath.Join(base, "kustomization.yaml"), []byte(strings.Replace(kustomization, "deployment.yaml", "../../../../etc/passwd", 1)), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := RenderKustomize(root, "deploy/environments/production"); err == nil {
		t.Fatal("escaping resource accepted")
	}
}

func FuzzKustomizePathNeverEscapesRepository(f *testing.F) {
	f.Add("deploy/environments/production")
	f.Add("../../etc")
	f.Add("deploy\\production")
	f.Fuzz(func(t *testing.T, path string) {
		if len(path) > 1024 || strings.ContainsRune(path, '\x00') {
			t.Skip()
		}
		root := t.TempDir()
		_, _ = RenderKustomize(root, path)
	})
}
