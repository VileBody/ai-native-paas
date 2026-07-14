package architecture_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestEveryEntrypointDeclaresProductionAdapterInventory(t *testing.T) {
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve repository root")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(filename), "..", ".."))
	entrypoints, err := filepath.Glob(filepath.Join(root, "cmd", "*", "main.go"))
	if err != nil || len(entrypoints) == 0 {
		t.Fatalf("discover entrypoints: files=%d err=%v", len(entrypoints), err)
	}
	for _, entrypoint := range entrypoints {
		raw, err := os.ReadFile(entrypoint)
		if err != nil {
			t.Fatal(err)
		}
		source := string(raw)
		legacyInventory := strings.Contains(source, "platformprofile.Validate(os.Getenv(\"PLATFORM_PROFILE\")")
		dynamicInventory := strings.Contains(source, "platformprofile.Parse(os.Getenv(\"PLATFORM_PROFILE\"))") &&
			strings.Contains(source, "platformprofile.Validate(string(profile)")
		if !legacyInventory && !dynamicInventory {
			t.Errorf("%s has no fail-closed production adapter inventory", filepath.Base(filepath.Dir(entrypoint)))
		}
	}
}

func TestProductionHumanAPIEntrypointsBootstrapPostgresOIDC(t *testing.T) {
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve repository root")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(filename), "..", ".."))
	for _, api := range []string{"commerce-api", "kernel-api", "project-api", "source-api"} {
		raw, err := os.ReadFile(filepath.Join(root, "cmd", api, "main.go"))
		if err != nil {
			t.Fatal(err)
		}
		source := string(raw)
		for _, required := range []string{
			"oidcverify.NewPostgresVerifier(",
			"OIDC:",
			"oidcVerifier",
			"oidc-jwks-verifier",
			"postgres-membership-resolver",
		} {
			if !strings.Contains(source, required) {
				t.Errorf("%s does not wire production identity component %q", api, required)
			}
		}
	}
}
