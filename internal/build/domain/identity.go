package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path"
	"sort"
	"strings"

	sourcev1 "github.com/keir-research/ai-native-paas/pkg/contracts/source/v1"
)

type BuildType string

const (
	BuildTypeAuto       BuildType = "auto"
	BuildTypeDockerfile BuildType = "dockerfile"
)

type BuildConfig struct {
	Type           BuildType         `json:"type"`
	Runtime        string            `json:"runtime,omitempty"`
	SourceRoot     string            `json:"source_root,omitempty"`
	DockerfilePath string            `json:"dockerfile_path,omitempty"`
	BuildCommand   []string          `json:"build_command,omitempty"`
	BuildEnv       map[string]string `json:"build_env,omitempty"`
	BuildSecretRef []string          `json:"build_secret_refs,omitempty"`
}

type identityInput struct {
	ProjectID            string           `json:"project_id"`
	RepositoryID         string           `json:"repository_id"`
	CommitSHA            string           `json:"commit_sha"`
	SourceRoot           string           `json:"source_root"`
	Config               normalizedConfig `json:"config"`
	BuilderDigest        string           `json:"builder_digest"`
	RunImageDigest       string           `json:"run_image_digest"`
	PlatformBuildVersion string           `json:"platform_build_version"`
}

type normalizedConfig struct {
	Type           BuildType      `json:"type"`
	Runtime        string         `json:"runtime,omitempty"`
	SourceRoot     string         `json:"source_root,omitempty"`
	DockerfilePath string         `json:"dockerfile_path,omitempty"`
	BuildCommand   []string       `json:"build_command,omitempty"`
	BuildEnv       []normalizedKV `json:"build_env,omitempty"`
	BuildSecretRef []string       `json:"build_secret_refs,omitempty"`
}

type normalizedKV struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

func NormalizeConfig(config BuildConfig) (BuildConfig, error) {
	if config.Type == "" {
		config.Type = BuildTypeAuto
	}
	if config.Type != BuildTypeAuto && config.Type != BuildTypeDockerfile {
		return BuildConfig{}, NewError(CodeInvalidArgument, "unsupported build type")
	}
	config.Runtime = strings.ToLower(strings.TrimSpace(config.Runtime))
	var err error
	config.SourceRoot, err = normalizeRelativePath(config.SourceRoot, true)
	if err != nil {
		return BuildConfig{}, NewError(CodeInvalidArgument, "source root escapes repository")
	}
	if config.DockerfilePath == "" && config.Type == BuildTypeDockerfile {
		config.DockerfilePath = "Dockerfile"
	}
	config.DockerfilePath, err = normalizeRelativePath(config.DockerfilePath, true)
	if err != nil {
		return BuildConfig{}, NewError(CodeInvalidArgument, "dockerfile path escapes source root")
	}
	if config.Type == BuildTypeDockerfile && config.DockerfilePath == "" {
		return BuildConfig{}, NewError(CodeInvalidArgument, "dockerfile path is required")
	}
	env := make(map[string]string, len(config.BuildEnv))
	for key, value := range config.BuildEnv {
		if key != strings.TrimSpace(key) || !validEnvKey(key) || strings.ContainsRune(value, '\x00') {
			return BuildConfig{}, NewError(CodeInvalidArgument, "invalid build environment")
		}
		if looksSensitive(strings.ToUpper(key)) {
			return BuildConfig{}, NewError(CodeInvalidArgument, "secret values must use build secret references")
		}
		env[key] = value
	}
	if len(env) == 0 {
		env = nil
	}
	config.BuildEnv = env
	refs := append([]string(nil), config.BuildSecretRef...)
	for i := range refs {
		refs[i] = strings.TrimSpace(refs[i])
		if refs[i] == "" || strings.ContainsAny(refs[i], "\x00\n\r") {
			return BuildConfig{}, NewError(CodeInvalidArgument, "invalid build secret reference")
		}
	}
	sort.Strings(refs)
	config.BuildSecretRef = dedupe(refs)
	commands := append([]string(nil), config.BuildCommand...)
	for _, item := range commands {
		if item == "" || strings.ContainsRune(item, '\x00') {
			return BuildConfig{}, NewError(CodeInvalidArgument, "invalid build command")
		}
	}
	config.BuildCommand = commands
	return config, nil
}

// NormalizeBuildInput resolves source_root to one canonical location. The
// revision owns the effective root; the config copy is cleared so fetch,
// detection, build and SBOM cannot accidentally apply the root twice.
func NormalizeBuildInput(source sourcev1.SourceRevision, config BuildConfig) (sourcev1.SourceRevision, BuildConfig, error) {
	if err := source.Validate(); err != nil {
		return sourcev1.SourceRevision{}, BuildConfig{}, NewError(CodeInvalidArgument, "invalid source revision")
	}
	normalizedConfig, err := NormalizeConfig(config)
	if err != nil {
		return sourcev1.SourceRevision{}, BuildConfig{}, err
	}
	revisionRoot, err := normalizeRelativePath(source.SourceRoot, true)
	if err != nil {
		return sourcev1.SourceRevision{}, BuildConfig{}, NewError(CodeInvalidArgument, "source root escapes repository")
	}
	configRoot := normalizedConfig.SourceRoot
	if revisionRoot != "" && configRoot != "" && revisionRoot != configRoot {
		return sourcev1.SourceRevision{}, BuildConfig{}, NewError(CodeInvalidArgument, "source revision and build config specify different source roots")
	}
	if configRoot != "" {
		revisionRoot = configRoot
	}
	source.SourceRoot = revisionRoot
	normalizedConfig.SourceRoot = ""
	return source, normalizedConfig, nil
}

func ComputeBuildIdentity(source sourcev1.SourceRevision, config BuildConfig, builderDigest, runImageDigest, platformBuildVersion string) (string, error) {
	var err error
	source, config, err = NormalizeBuildInput(source, config)
	if err != nil {
		return "", err
	}
	if !validDigest(builderDigest) || !validDigest(runImageDigest) || strings.TrimSpace(platformBuildVersion) == "" {
		return "", NewError(CodeInvalidArgument, "builder, run image and platform version are required")
	}
	root := source.SourceRoot
	input := identityInput{
		ProjectID:            source.ProjectID,
		RepositoryID:         source.RepositoryID,
		CommitSHA:            strings.ToLower(source.CommitSHA),
		SourceRoot:           root,
		Config:               canonicalConfig(config),
		BuilderDigest:        builderDigest,
		RunImageDigest:       runImageDigest,
		PlatformBuildVersion: strings.TrimSpace(platformBuildVersion),
	}
	raw, err := json.Marshal(input)
	if err != nil {
		return "", Wrap(CodePlatformFailure, "encode build identity", err)
	}
	sum := sha256.Sum256(raw)
	return "bldid_" + hex.EncodeToString(sum[:]), nil
}

func canonicalConfig(config BuildConfig) normalizedConfig {
	keys := make([]string, 0, len(config.BuildEnv))
	for key := range config.BuildEnv {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	env := make([]normalizedKV, 0, len(keys))
	for _, key := range keys {
		env = append(env, normalizedKV{Key: key, Value: config.BuildEnv[key]})
	}
	return normalizedConfig{
		Type:           config.Type,
		Runtime:        config.Runtime,
		SourceRoot:     config.SourceRoot,
		DockerfilePath: config.DockerfilePath,
		BuildCommand:   append([]string(nil), config.BuildCommand...),
		BuildEnv:       env,
		BuildSecretRef: append([]string(nil), config.BuildSecretRef...),
	}
}

func normalizeRelativePath(value string, allowEmpty bool) (string, error) {
	value = strings.TrimSpace(strings.ReplaceAll(value, "\\", "/"))
	if value == "" || value == "." {
		if allowEmpty {
			return "", nil
		}
		return "", NewError(CodeInvalidArgument, "relative path is required")
	}
	if strings.ContainsRune(value, '\x00') || strings.HasPrefix(value, "/") || path.IsAbs(value) || hasWindowsDrivePrefix(value) {
		return "", NewError(CodeInvalidArgument, "path must be relative")
	}
	for _, segment := range strings.Split(value, "/") {
		if segment == ".." {
			return "", NewError(CodeInvalidArgument, "path traversal is not allowed")
		}
	}
	cleaned := path.Clean(value)
	if cleaned == ".." || strings.HasPrefix(cleaned, "../") || cleaned == "." {
		return "", NewError(CodeInvalidArgument, "path traversal is not allowed")
	}
	return cleaned, nil
}
func hasWindowsDrivePrefix(value string) bool {
	return len(value) >= 2 && ((value[0] >= 'A' && value[0] <= 'Z') || (value[0] >= 'a' && value[0] <= 'z')) && value[1] == ':'
}
func validEnvKey(value string) bool {
	if value == "" || !((value[0] >= 'A' && value[0] <= 'Z') || (value[0] >= 'a' && value[0] <= 'z') || value[0] == '_') {
		return false
	}
	for i := 1; i < len(value); i++ {
		c := value[i]
		if !((c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '_') {
			return false
		}
	}
	return true
}

func validDigest(v string) bool {
	if !strings.HasPrefix(v, "sha256:") || len(v) != 71 {
		return false
	}
	for _, c := range v[7:] {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
func looksSensitive(key string) bool {
	for _, fragment := range []string{"SECRET", "TOKEN", "PASSWORD", "PRIVATE_KEY", "API_KEY", "CREDENTIAL"} {
		if strings.Contains(key, fragment) {
			return true
		}
	}
	return false
}
func dedupe(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := values[:0]
	for _, value := range values {
		if len(out) == 0 || out[len(out)-1] != value {
			out = append(out, value)
		}
	}
	return out
}
