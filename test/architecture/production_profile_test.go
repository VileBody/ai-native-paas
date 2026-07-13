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
		if !strings.Contains(string(raw), "platformprofile.Validate(os.Getenv(\"PLATFORM_PROFILE\")") {
			t.Errorf("%s has no fail-closed production adapter inventory", filepath.Base(filepath.Dir(entrypoint)))
		}
	}
}
