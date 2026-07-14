package dockerfilepolicy_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/keir-research/ai-native-paas/internal/build/dockerfilepolicy"
	"github.com/keir-research/ai-native-paas/internal/build/domain"
)

func TestBuild_MutableBaseOrOutputTagCannotDefineProductionArtifact(t *testing.T) {
	root := t.TempDir()
	writeDockerfile := func(document string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, "Dockerfile"), []byte(document), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, mutable := range []string{
		"FROM ubuntu:latest\n", "FROM ubuntu:24.04\n", "ARG BASE=ubuntu:latest\nFROM $BASE\n",
	} {
		writeDockerfile(mutable)
		if err := dockerfilepolicy.Validate(root, "Dockerfile"); !domain.HasCode(err, domain.CodePolicyRejected) {
			t.Fatalf("mutable=%q err=%v", mutable, err)
		}
	}
	pinned := "FROM golang@sha256:" + strings.Repeat("a", 64) + " AS build\nRUN true\nFROM build AS output\n"
	writeDockerfile(pinned)
	if err := dockerfilepolicy.Validate(root, "Dockerfile"); err != nil {
		t.Fatal(err)
	}
	if _, err := domain.NewArtifact("artifact", "tenant", "build", "registry.test/tenants/tenant/apps/project", "latest", "application/vnd.oci.image.manifest.v1+json", time.Now()); !domain.HasCode(err, domain.CodeInvalidArgument) {
		t.Fatalf("mutable output tag err=%v", err)
	}
}

func TestDockerfilePolicy_RejectsDefinitionSymlink(t *testing.T) {
	root := t.TempDir()
	external := filepath.Join(t.TempDir(), "Dockerfile")
	if err := os.WriteFile(external, []byte("FROM scratch\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, filepath.Join(root, "Dockerfile")); err != nil {
		t.Fatal(err)
	}
	if err := dockerfilepolicy.Validate(root, "Dockerfile"); !domain.HasCode(err, domain.CodePolicyRejected) {
		t.Fatalf("err=%v", err)
	}
}
