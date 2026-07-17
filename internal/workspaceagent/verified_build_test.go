package workspaceagent

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	buildv2 "github.com/keir-research/ai-native-paas/pkg/contracts/build/v2"
)

func TestVerifiedBuild_RequiresCanonicalSpecExactSourceAndDockerfile(t *testing.T) {
	sha := strings.Repeat("a", 40)
	spec := buildv2.BuildSpec{
		SourceSHA: sha, Driver: buildv2.DriverDockerfile, DefinitionPath: "Dockerfile",
		Platforms: []string{"linux/amd64"}, NetworkProfile: "governed", CacheScope: "project-1",
		ResourceClass: "small", TimeoutSeconds: 600,
	}
	canonical, fingerprint, err := validateVerifiedBuildArguments([]string{sha, buildSpecJSON(t, spec)})
	if err != nil || canonical.SourceSHA != sha || canonical.Driver != buildv2.DriverDockerfile || !strings.HasPrefix(fingerprint, "sha256:") {
		t.Fatalf("canonical=%+v fingerprint=%q err=%v", canonical, fingerprint, err)
	}
	spec.SourceSHA = strings.Repeat("b", 40)
	if _, _, err := validateVerifiedBuildArguments([]string{sha, buildSpecJSON(t, spec)}); err == nil {
		t.Fatal("mismatched source SHA accepted")
	}
	spec.SourceSHA = sha
	spec.Driver = buildv2.DriverBuildpacks
	if _, _, err := validateVerifiedBuildArguments([]string{sha, buildSpecJSON(t, spec)}); err == nil {
		t.Fatal("non-Dockerfile driver accepted")
	}
	unknown := `{"source_sha":"` + sha + `","driver":"dockerfile","definition_path":"Dockerfile","platforms":["linux/amd64"],"network_profile":"governed","cache_scope":"project-1","resource_class":"small","timeout_seconds":600,"surprise":true}`
	if _, _, err := validateVerifiedBuildArguments([]string{sha, unknown}); err == nil {
		t.Fatal("unknown build spec field accepted")
	}
}

func TestVerifiedBuild_BuildKitArgumentsAreRootlessDigestOnlyAndDeterministic(t *testing.T) {
	sha := strings.Repeat("a", 40)
	spec := buildv2.BuildSpec{
		SourceSHA: sha, Driver: buildv2.DriverDockerfile, DefinitionPath: "docker/Dockerfile",
		Platforms:      []string{"linux/arm64", "linux/amd64"},
		BuildArguments: map[string]string{"VERSION": "1", "MODE": "release"},
		NetworkProfile: "governed", CacheScope: "project-1", ResourceClass: "small", TimeoutSeconds: 600,
	}
	canonical, err := spec.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	metadataFile := filepath.Join(t.TempDir(), "metadata.json")
	args, err := buildkitArguments(canonical, "harbor.internal/tenant/app", metadataFile)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, "\n")
	for _, required := range []string{
		"--addr\nunix:///run/workspace-buildkit/buildkitd.sock",
		"--frontend\ndockerfile.v0",
		"--local\ncontext=.",
		"--local\ndockerfile=docker",
		"--opt\nfilename=Dockerfile",
		"--metadata-file\n" + metadataFile,
		"--output\ntype=image,name=harbor.internal/tenant/app:sha-aaaaaaaaaaaa-",
		"--opt\nbuild-arg:MODE=release\n--opt\nbuild-arg:VERSION=1",
	} {
		if !strings.Contains(joined, required) {
			t.Fatalf("BuildKit args missing %q in:\n%s", required, joined)
		}
	}
	if strings.Contains(joined, "@sha256:") {
		t.Fatalf("BuildKit push target unexpectedly used digest as mutable output name:\n%s", joined)
	}
}

func TestVerifiedBuild_ExecutesFakeBuildKitAndReadsImmutableDigest(t *testing.T) {
	sha := initVerifiedBuildRepository(t)
	fakeBin := t.TempDir()
	argsFile := filepath.Join(t.TempDir(), "buildctl.args")
	metadataDigest := "sha256:" + strings.Repeat("d", 64)
	buildctl := filepath.Join(fakeBin, "buildctl")
	script := `#!/bin/sh
set -eu
out=""
prev=""
for arg in "$@"; do
  if [ "$prev" = "--metadata-file" ]; then out="$arg"; fi
  prev="$arg"
done
if [ -z "$out" ]; then exit 17; fi
printf '%s\n' "$*" > "$BUILDKIT_ARGS_FILE"
printf '{"containerimage.digest":"` + metadataDigest + `"}' > "$out"
`
	if err := os.WriteFile(buildctl, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("BUILDKIT_ARGS_FILE", argsFile)
	t.Setenv("PLATFORM_BUILD_REPOSITORY", "harbor.internal/tenant/app")
	spec := buildv2.BuildSpec{
		SourceSHA: sha, Driver: buildv2.DriverDockerfile, DefinitionPath: "Dockerfile",
		Platforms: []string{"linux/amd64"}, NetworkProfile: "governed", CacheScope: "project-1",
		ResourceClass: "small", TimeoutSeconds: 600,
	}
	if err := ExecuteVerifiedBuild([]string{sha, buildSpecJSON(t, spec)}); err != nil {
		t.Fatal(err)
	}
	rawArgs, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(rawArgs), "type=image,name=harbor.internal/tenant/app:sha-"+sha[:12]) {
		t.Fatalf("fake BuildKit args=%s", rawArgs)
	}
}

func TestVerifiedBuild_RejectsDirtySourceBeforeBuildKit(t *testing.T) {
	sha := initVerifiedBuildRepository(t)
	if err := os.WriteFile("untracked.txt", []byte("dirty"), 0o600); err != nil {
		t.Fatal(err)
	}
	spec := buildv2.BuildSpec{
		SourceSHA: sha, Driver: buildv2.DriverDockerfile, DefinitionPath: "Dockerfile",
		Platforms: []string{"linux/amd64"}, NetworkProfile: "governed", CacheScope: "project-1",
		ResourceClass: "small", TimeoutSeconds: 600,
	}
	if err := ExecuteVerifiedBuild([]string{sha, buildSpecJSON(t, spec)}); err == nil || !strings.Contains(err.Error(), "source tree is not clean") {
		t.Fatalf("dirty source err=%v", err)
	}
}

func buildSpecJSON(t *testing.T, spec buildv2.BuildSpec) string {
	t.Helper()
	raw, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func initVerifiedBuildRepository(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	t.Chdir(root)
	runGitTest(t, "init", "--initial-branch=main")
	if err := os.WriteFile("Dockerfile", []byte("FROM scratch\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, "add", "Dockerfile")
	runGitTest(t, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-m", "initial")
	out := runGitTest(t, "rev-parse", "--verify", "HEAD^{commit}")
	return strings.TrimSpace(out)
}

func runGitTest(t *testing.T, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	out, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}
