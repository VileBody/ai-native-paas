package workspaceagent

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/keir-research/ai-native-paas/internal/build/dockerfilepolicy"
	buildv1 "github.com/keir-research/ai-native-paas/pkg/contracts/build/v1"
	buildv2 "github.com/keir-research/ai-native-paas/pkg/contracts/build/v2"
)

const maxVerifiedBuildSpecBytes = 64 << 10

func ExecuteVerifiedBuild(arguments []string) error {
	spec, specDigest, err := validateVerifiedBuildArguments(arguments)
	if err != nil {
		return err
	}
	if err := verifyCleanBuildSource(spec.SourceSHA); err != nil {
		return err
	}
	root, err := workspaceRepositoryRoot()
	if err != nil {
		return err
	}
	contextDir := root
	if spec.ContextRoot != "" {
		contextDir = filepath.Join(root, filepath.FromSlash(spec.ContextRoot))
	}
	if err := dockerfilepolicy.Validate(contextDir, spec.DefinitionPath); err != nil {
		return fmt.Errorf("validate Dockerfile policy: %w", err)
	}
	repository := strings.TrimSpace(os.Getenv("PLATFORM_BUILD_REPOSITORY"))
	if !validBuildRepository(repository) {
		return errors.New("verified build repository destination is unavailable")
	}
	buildctl, err := exec.LookPath("buildctl")
	if err != nil {
		return errors.New("BuildKit client is unavailable")
	}
	metadataFile, cleanup, err := verifiedBuildMetadataFile()
	if err != nil {
		return err
	}
	defer cleanup()
	args, err := buildkitArguments(spec, repository, metadataFile)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(spec.TimeoutSeconds)*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, buildctl, args...)
	command.Dir = contextDir
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	if err := command.Run(); err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return errors.New("BuildKit build timed out")
		}
		return errors.New("BuildKit build failed")
	}
	digest, err := readBuildKitMetadataDigest(metadataFile)
	if err != nil {
		return err
	}
	result := buildv2.VerifiedBuildReceipt{
		SourceSHA: spec.SourceSHA, SpecDigest: specDigest, Repository: repository, Digest: digest,
		MediaType: "application/vnd.oci.image.manifest.v1+json", CapturedAt: time.Now().UTC(),
		Builder: "rootless-buildkit", BuilderAddr: buildkitAddress(),
	}
	return json.NewEncoder(os.Stdout).Encode(result)
}

func validateVerifiedBuildArguments(arguments []string) (buildv2.BuildSpec, string, error) {
	if len(arguments) != 2 || !commitSHAPattern.MatchString(arguments[0]) || len(arguments[1]) == 0 || len(arguments[1]) > maxVerifiedBuildSpecBytes {
		return buildv2.BuildSpec{}, "", errors.New("exact source revision and bounded build spec are required")
	}
	var spec buildv2.BuildSpec
	decoder := json.NewDecoder(strings.NewReader(arguments[1]))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&spec); err != nil || ensureJSONEOF(decoder) != nil {
		return buildv2.BuildSpec{}, "", errors.New("invalid build spec")
	}
	canonical, err := spec.Canonical()
	if err != nil {
		return buildv2.BuildSpec{}, "", errors.New("invalid canonical build spec")
	}
	if canonical.SourceSHA != arguments[0] {
		return buildv2.BuildSpec{}, "", errors.New("build spec source revision does not match command binding")
	}
	if canonical.Driver != buildv2.DriverDockerfile {
		return buildv2.BuildSpec{}, "", errors.New("only Dockerfile builds are executable in this workspace slice")
	}
	if len(canonical.SecretRefs) != 0 {
		return buildv2.BuildSpec{}, "", errors.New("BuildKit secret broker is not wired for verified-build yet")
	}
	fingerprint, err := canonical.Fingerprint()
	if err != nil {
		return buildv2.BuildSpec{}, "", errors.New("fingerprint canonical build spec")
	}
	return canonical, fingerprint, nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

func verifyCleanBuildSource(expected string) error {
	git, err := exec.LookPath("git")
	if err != nil {
		return errors.New("Git executable is unavailable")
	}
	rawHead, err := exec.Command(git, "rev-parse", "--verify", "HEAD^{commit}").Output()
	actual := strings.TrimSpace(string(rawHead))
	if err != nil || subtle.ConstantTimeCompare([]byte(actual), []byte(expected)) != 1 {
		return errors.New("workspace source revision does not match build binding")
	}
	rawStatus, err := exec.Command(git, "status", "--porcelain=v1", "-z", "--untracked-files=normal").Output()
	if err != nil || len(bytes.Trim(rawStatus, "\x00")) != 0 {
		return errors.New("workspace source tree is not clean for verified build")
	}
	return nil
}

func workspaceRepositoryRoot() (string, error) {
	git, err := exec.LookPath("git")
	if err != nil {
		return "", errors.New("Git executable is unavailable")
	}
	rawRoot, err := exec.Command(git, "rev-parse", "--show-toplevel").Output()
	root := strings.TrimSpace(string(rawRoot))
	if err != nil || !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return "", errors.New("workspace repository root is unavailable")
	}
	return root, nil
}

func verifiedBuildMetadataFile() (string, func(), error) {
	if configured := strings.TrimSpace(os.Getenv("PLATFORM_BUILD_METADATA_FILE")); configured != "" {
		if !filepath.IsAbs(configured) || filepath.Clean(configured) != configured || strings.ContainsRune(configured, '\x00') {
			return "", func() {}, errors.New("BuildKit metadata path is invalid")
		}
		return configured, func() {}, nil
	}
	file, err := os.CreateTemp("", "verified-build-metadata-*.json")
	if err != nil {
		return "", func() {}, errors.New("create BuildKit metadata file")
	}
	name := file.Name()
	if err := file.Close(); err != nil {
		_ = os.Remove(name)
		return "", func() {}, errors.New("prepare BuildKit metadata file")
	}
	return name, func() { _ = os.Remove(name) }, nil
}

func buildkitArguments(spec buildv2.BuildSpec, repository, metadataFile string) ([]string, error) {
	if !validBuildRepository(repository) || !filepath.IsAbs(metadataFile) || filepath.Clean(metadataFile) != metadataFile {
		return nil, errors.New("invalid BuildKit output binding")
	}
	dockerfileDirectory := filepath.ToSlash(filepath.Dir(spec.DefinitionPath))
	if dockerfileDirectory == "." {
		dockerfileDirectory = "."
	}
	tag := "sha-" + spec.SourceSHA[:12] + "-" + strings.TrimPrefix(specDigestForTag(spec), "sha256:")[:12]
	args := []string{
		"--addr", buildkitAddress(),
		"build",
		"--frontend", "dockerfile.v0",
		"--local", "context=.",
		"--local", "dockerfile=" + dockerfileDirectory,
		"--opt", "filename=" + filepath.ToSlash(filepath.Base(spec.DefinitionPath)),
		"--opt", "platform=" + strings.Join(spec.Platforms, ","),
		"--metadata-file", metadataFile,
		"--output", "type=image,name=" + repository + ":" + tag + ",push=true",
	}
	if len(spec.BuildArguments) != 0 {
		names := make([]string, 0, len(spec.BuildArguments))
		for name := range spec.BuildArguments {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			args = append(args, "--opt", "build-arg:"+name+"="+spec.BuildArguments[name])
		}
	}
	return args, nil
}

func specDigestForTag(spec buildv2.BuildSpec) string {
	fingerprint, err := spec.Fingerprint()
	if err != nil {
		return "sha256:" + strings.Repeat("0", 64)
	}
	return fingerprint
}

func buildkitAddress() string {
	if value := strings.TrimSpace(os.Getenv("BUILDKIT_HOST")); value != "" {
		return value
	}
	return "unix:///run/workspace-buildkit/buildkitd.sock"
}

func validBuildRepository(repository string) bool {
	repository = strings.TrimSpace(repository)
	return repository != "" && len(repository) <= 512 && !strings.ContainsAny(repository, "\x00\r\n\t @") && !strings.Contains(repository, "@sha256:")
}

func readBuildKitMetadataDigest(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", errors.New("open BuildKit metadata")
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, (1<<20)+1))
	if err != nil || len(raw) > 1<<20 {
		return "", errors.New("read BuildKit metadata")
	}
	var metadata map[string]any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&metadata); err != nil || ensureJSONEOF(decoder) != nil {
		return "", errors.New("decode BuildKit metadata")
	}
	digest, _ := metadata["containerimage.digest"].(string)
	if !buildv1.ValidDigest(digest) {
		return "", errors.New("BuildKit metadata lacks immutable image digest")
	}
	return digest, nil
}
