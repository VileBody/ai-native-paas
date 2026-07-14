package pivot_test

import (
	"strings"
	"testing"

	"github.com/keir-research/ai-native-paas/internal/build/domain"
	buildv2 "github.com/keir-research/ai-native-paas/pkg/contracts/build/v2"
)

func TestBuild_IdentityIncludesCommitAndCanonicalBuildSpec(t *testing.T) {
	argumentsA := map[string]string{"VERSION": "1", "MODE": "release"}
	argumentsB := map[string]string{"MODE": "release", "VERSION": "1"}
	a := buildv2.BuildSpec{
		SourceSHA: strings.ToUpper(strings.Repeat("a", 40)), SourceRoot: " ./services/api/ ",
		Driver: buildv2.DriverDockerfile, DefinitionPath: "./docker/Dockerfile",
		Platforms: []string{" linux/arm64 ", "linux/amd64", "linux/amd64"}, BuildArguments: argumentsA,
		SecretRefs: []string{" registry ", "npm", "npm"}, NetworkPolicy: " GOVERNED ", CacheScope: " project-1 ",
		TimeoutSeconds: 900,
	}
	b := buildv2.BuildSpec{
		SourceSHA: strings.Repeat("a", 40), ContextRoot: "services/api",
		Driver: buildv2.DriverDockerfile, DefinitionPath: "docker/Dockerfile",
		Platforms: []string{"linux/amd64", "linux/arm64"}, BuildArguments: argumentsB,
		SecretRefs: []string{"npm", "registry"}, NetworkProfile: "governed", CacheScope: "project-1",
		ResourceClass: "standard", TimeoutSeconds: 900,
	}
	identityA, err := domain.ComputeBuildV2Identity("project-1", "repository-1", a)
	if err != nil {
		t.Fatal(err)
	}
	identityB, err := domain.ComputeBuildV2Identity("project-1", "repository-1", b)
	if err != nil || identityA != identityB {
		t.Fatalf("canonical identities differ: a=%s b=%s err=%v", identityA, identityB, err)
	}
	canonical, err := a.Canonical()
	if err != nil || canonical.SourceRoot != "" || canonical.ContextRoot != "services/api" || canonical.NetworkPolicy != "" || canonical.NetworkProfile != "governed" || canonical.ResourceClass != "standard" || len(canonical.Platforms) != 2 || len(canonical.SecretRefs) != 2 {
		t.Fatalf("canonical spec=%+v err=%v", canonical, err)
	}

	variants := []buildv2.BuildSpec{b, b, b, b}
	variants[0].DefinitionPath = "docker/Release.Dockerfile"
	variants[1].NetworkProfile = "deny-all"
	variants[2].Platforms = []string{"linux/amd64"}
	variants[3].SourceSHA = strings.Repeat("b", 40)
	for index, variant := range variants {
		identity, err := domain.ComputeBuildV2Identity("project-1", "repository-1", variant)
		if err != nil {
			t.Fatalf("variant %d: %v", index, err)
		}
		if identity == identityA {
			t.Fatalf("variant %d did not change build identity", index)
		}
	}
	otherRepository, err := domain.ComputeBuildV2Identity("project-1", "repository-2", b)
	if err != nil || otherRepository == identityA {
		t.Fatalf("repository identity not bound: identity=%s err=%v", otherRepository, err)
	}
}
