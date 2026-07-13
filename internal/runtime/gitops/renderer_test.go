package gitops

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/keir-research/ai-native-paas/internal/runtime/application"
	"github.com/keir-research/ai-native-paas/internal/runtime/domain"
	"github.com/keir-research/ai-native-paas/internal/runtime/testkit"
	runtimev1 "github.com/keir-research/ai-native-paas/pkg/contracts/runtime/v1"
)

func renderFixture(t *testing.T) application.RenderedBundle {
	t.Helper()
	now := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
	app, err := domain.NewApplication("app-1", "tenant-1", "project-1", "booking", now)
	if err != nil {
		t.Fatal(err)
	}
	env, err := domain.NewEnvironment("env-1", "tenant-1", app.ID, "production", true, now)
	if err != nil {
		t.Fatal(err)
	}
	cell, err := domain.NewRuntimeCell("cell-a", "eu1", []runtimev1.IsolationClass{runtimev1.IsolationSandboxed}, 100, "https://git.test/runtime-cell-a.git", "https://cluster-a.test", "runtime-cell", "apps.eu1.test", now)
	if err != nil {
		t.Fatal(err)
	}
	placement, err := domain.NewPlacement("plc-1", "tenant-1", env.ID, cell, runtimev1.IsolationSandboxed, 1, now)
	if err != nil {
		t.Fatal(err)
	}
	config := testkit.Config()
	config.GeneratedHostname = "app-1-production.apps.eu1.test"
	config.AttachmentSnapshotRef = "secset-app-1-production-v3"
	release, err := domain.NewRelease("rel-1", "tenant-1", app.ID, env.ID, testkit.Artifact("a"), config, "policy-v1", now)
	if err != nil {
		t.Fatal(err)
	}
	if err := release.Transition(runtimev1.ReleaseValidated, now); err != nil {
		t.Fatal(err)
	}
	bundle, err := (Renderer{}).Render(application.RenderInput{Application: app, Environment: env, Release: release, Placement: placement, Cell: cell})
	if err != nil {
		t.Fatal(err)
	}
	return bundle
}

func TestRenderer_SameReleaseProducesByteStableManifest(t *testing.T) {
	a := renderFixture(t)
	b := renderFixture(t)
	if a.ManifestHash != b.ManifestHash || !bytes.Equal(BundleBytes(a), BundleBytes(b)) {
		t.Fatalf("render is not deterministic: %s != %s", a.ManifestHash, b.ManifestHash)
	}
}

func TestRenderer_ContainsDigestNotMutableTag(t *testing.T) {
	bundle := renderFixture(t)
	raw := string(bundle.Files["paasapp.yaml"])
	if !strings.Contains(raw, `"digest": "sha256:`) || strings.Contains(raw, `"tag"`) || strings.Contains(raw, ":latest") {
		t.Fatalf("manifest is not digest-pinned: %s", raw)
	}
}

func TestRenderer_ContainsOnlyPaaSAppAndNamespace(t *testing.T) {
	bundle := renderFixture(t)
	if len(bundle.Files) != 2 || bundle.Files["namespace.yaml"] == nil || bundle.Files["paasapp.yaml"] == nil {
		t.Fatalf("unexpected files: %#v", bundle.Files)
	}
	var kinds []string
	for _, name := range []string{"namespace.yaml", "paasapp.yaml"} {
		var meta struct {
			Kind string `json:"kind"`
		}
		if err := json.Unmarshal(bundle.Files[name], &meta); err != nil {
			t.Fatal(err)
		}
		kinds = append(kinds, meta.Kind)
	}
	if strings.Join(kinds, ",") != "Namespace,PaaSApp" {
		t.Fatalf("kinds=%v", kinds)
	}
}

func TestRenderer_DoesNotSerializeSecretValues(t *testing.T) {
	bundle := renderFixture(t)
	raw := BundleBytes(bundle)
	for _, forbidden := range []string{"DATABASE_URL=", "super-secret-value", `"secretValue"`, `"password"`} {
		if bytes.Contains(raw, []byte(forbidden)) {
			t.Fatalf("secret material leaked: %q", forbidden)
		}
	}
	if !bytes.Contains(raw, []byte("secset-app-1-production-v3")) {
		t.Fatal("attachment reference must be serialized")
	}
}

func TestRenderer_PathIsCellTenantAppEnvironment(t *testing.T) {
	bundle := renderFixture(t)
	want := "cells/cell-a/tenants/tenant-1/apps/app-1/production"
	if bundle.Path != want {
		t.Fatalf("path=%q want=%q", bundle.Path, want)
	}
	if filepath.IsAbs(bundle.Path) || strings.Contains(bundle.Path, "..") {
		t.Fatalf("unsafe path: %q", bundle.Path)
	}
}

func TestRenderer_GoldenFileMatchesV1alpha1Contract(t *testing.T) {
	bundle := renderFixture(t)
	actual := BundleBytes(bundle)
	path := filepath.Join("testdata", "paasapp-v1alpha1.golden")
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(path, actual, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	expected, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(actual, expected) {
		t.Fatalf("golden mismatch\n--- expected ---\n%s\n--- actual ---\n%s", expected, actual)
	}
}

func TestRenderer_RejectsUnsafeGitOpsPath(t *testing.T) {
	now := time.Now().UTC()
	app := domain.Application{ID: "app-1", TenantID: "../tenant", ProjectID: "project-1", Name: "booking", Lifecycle: runtimev1.LifecycleActive, Version: 1, CreatedAt: now, UpdatedAt: now}
	env, _ := domain.NewEnvironment("env-1", "../tenant", "app-1", "production", true, now)
	cell, _ := domain.NewRuntimeCell("cell-a", "eu1", []runtimev1.IsolationClass{runtimev1.IsolationSandboxed}, 100, "git", "cluster", "runtime-cell", "apps.eu1.test", now)
	placement := domain.Placement{ID: "plc-1", TenantID: "../tenant", EnvironmentID: env.ID, CellID: cell.ID, Region: cell.Region, Isolation: runtimev1.IsolationSandboxed, Current: true, Units: 1, Version: 1}
	config := testkit.Config()
	config.GeneratedHostname = "app-1-production.apps.eu1.test"
	release := domain.Release{ID: "rel-1", TenantID: "../tenant", ApplicationID: app.ID, EnvironmentID: env.ID, Artifact: testkit.Artifact("a"), Configuration: config, State: runtimev1.ReleaseValidated, Version: 1}
	_, err := (Renderer{}).Render(application.RenderInput{Application: app, Environment: env, Release: release, Placement: placement, Cell: cell})
	if err == nil {
		t.Fatal("unsafe path accepted")
	}
}
