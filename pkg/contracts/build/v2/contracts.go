// Package v2 defines explicit, immutable and verifiable build contracts.
package v2

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"
)

const APIVersion = "build.platform.example.com/v2"

var (
	digest  = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	fullSHA = regexp.MustCompile(`^[0-9a-fA-F]{40}([0-9a-fA-F]{24})?$`)
)

type Driver string

const (
	DriverDockerfile Driver = "dockerfile"
	DriverBuildpacks Driver = "buildpacks"
	DriverNix        Driver = "nix"
	DriverCustom     Driver = "custom-approved"
)

type BuildSpec struct {
	SourceSHA   string `json:"source_sha"`
	ContextRoot string `json:"context_root,omitempty"`
	// SourceRoot is the additive compatibility spelling used by early v2
	// clients. Canonical form always emits ContextRoot instead.
	SourceRoot     string            `json:"source_root,omitempty"`
	Driver         Driver            `json:"driver"`
	DefinitionPath string            `json:"definition_path,omitempty"`
	Platforms      []string          `json:"platforms"`
	BuildArguments map[string]string `json:"build_arguments,omitempty"`
	SecretRefs     []string          `json:"secret_refs,omitempty"`
	NetworkProfile string            `json:"network_profile,omitempty"`
	// NetworkPolicy is the additive compatibility spelling used by early v2
	// clients. Canonical form always emits NetworkProfile instead.
	NetworkPolicy  string `json:"network_policy,omitempty"`
	CacheScope     string `json:"cache_scope"`
	ResourceClass  string `json:"resource_class"`
	TimeoutSeconds int64  `json:"timeout_seconds"`
}

func (s BuildSpec) Validate() error {
	_, err := s.Canonical()
	return err
}

// Canonical returns the only representation used for fingerprints and build
// identities. It clones all mutable inputs and never carries secret values.
func (s BuildSpec) Canonical() (BuildSpec, error) {
	s.SourceSHA = strings.ToLower(strings.TrimSpace(s.SourceSHA))
	s.Driver = Driver(strings.ToLower(strings.TrimSpace(string(s.Driver))))
	s.NetworkProfile = strings.ToLower(strings.TrimSpace(s.NetworkProfile))
	legacyNetworkProfile := strings.ToLower(strings.TrimSpace(s.NetworkPolicy))
	if s.NetworkProfile == "" {
		s.NetworkProfile = legacyNetworkProfile
	} else if legacyNetworkProfile != "" && legacyNetworkProfile != s.NetworkProfile {
		return BuildSpec{}, errors.New("conflicting build network profiles")
	}
	s.NetworkPolicy = ""
	s.CacheScope = strings.TrimSpace(s.CacheScope)
	s.ResourceClass = strings.ToLower(strings.TrimSpace(s.ResourceClass))
	if s.ResourceClass == "" {
		s.ResourceClass = "standard"
	}
	contextRoot := strings.TrimSpace(s.ContextRoot)
	legacySourceRoot := strings.TrimSpace(s.SourceRoot)
	if contextRoot == "" {
		contextRoot = legacySourceRoot
	} else if legacySourceRoot != "" {
		canonicalContextRoot, contextErr := canonicalRelativePath(contextRoot, true)
		canonicalLegacyRoot, legacyErr := canonicalRelativePath(legacySourceRoot, true)
		if contextErr != nil || legacyErr != nil || canonicalContextRoot != canonicalLegacyRoot {
			return BuildSpec{}, errors.New("conflicting build context roots")
		}
	}
	var err error
	s.ContextRoot, err = canonicalRelativePath(contextRoot, true)
	if err != nil {
		return BuildSpec{}, errors.New("invalid build context root")
	}
	s.SourceRoot = ""
	s.DefinitionPath, err = canonicalRelativePath(s.DefinitionPath, true)
	if err != nil {
		return BuildSpec{}, errors.New("invalid build definition path")
	}
	switch s.Driver {
	case DriverDockerfile, DriverBuildpacks, DriverNix, DriverCustom:
	default:
		return BuildSpec{}, errors.New("invalid build driver")
	}
	if !fullSHA.MatchString(s.SourceSHA) || len(s.Platforms) == 0 || len(s.Platforms) > 8 || (s.NetworkProfile != "governed" && s.NetworkProfile != "deny-all") || s.CacheScope == "" || len(s.CacheScope) > 256 || s.TimeoutSeconds < 60 || s.TimeoutSeconds > 86400 {
		return BuildSpec{}, errors.New("invalid build specification")
	}
	if s.Driver == DriverDockerfile && s.DefinitionPath == "" {
		return BuildSpec{}, errors.New("Dockerfile path is required")
	}
	platforms := make([]string, 0, len(s.Platforms))
	for _, platform := range s.Platforms {
		platform = strings.ToLower(strings.TrimSpace(platform))
		if platform != "linux/amd64" && platform != "linux/arm64" {
			return BuildSpec{}, errors.New("unsupported build platform")
		}
		platforms = append(platforms, platform)
	}
	sort.Strings(platforms)
	s.Platforms = dedupeStrings(platforms)
	if len(s.Platforms) > 8 {
		return BuildSpec{}, errors.New("too many build platforms")
	}
	if len(s.BuildArguments) > 128 {
		return BuildSpec{}, errors.New("too many build arguments")
	}
	arguments := make(map[string]string, len(s.BuildArguments))
	for key, value := range s.BuildArguments {
		if key == "" || key != strings.TrimSpace(key) || len(key) > 128 || len(value) > 4096 || strings.ContainsAny(key+value, "\x00\r\n") {
			return BuildSpec{}, errors.New("invalid build argument")
		}
		arguments[key] = value
	}
	if len(arguments) == 0 {
		arguments = nil
	}
	s.BuildArguments = arguments
	if len(s.SecretRefs) > 128 {
		return BuildSpec{}, errors.New("too many build secret references")
	}
	secretRefs := make([]string, 0, len(s.SecretRefs))
	for _, ref := range s.SecretRefs {
		ref = strings.TrimSpace(ref)
		if ref == "" || len(ref) > 256 || strings.ContainsAny(ref, "\x00\r\n") {
			return BuildSpec{}, errors.New("invalid build secret reference")
		}
		secretRefs = append(secretRefs, ref)
	}
	sort.Strings(secretRefs)
	s.SecretRefs = dedupeStrings(secretRefs)
	switch s.ResourceClass {
	case "small", "standard", "large":
	default:
		return BuildSpec{}, errors.New("invalid build resource class")
	}
	return s, nil
}

func (s BuildSpec) Fingerprint() (string, error) {
	canonical, err := s.Canonical()
	if err != nil {
		return "", err
	}
	raw, err := json.Marshal(struct {
		APIVersion string    `json:"api_version"`
		Spec       BuildSpec `json:"spec"`
	}{APIVersion: APIVersion, Spec: canonical})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return fmt.Sprintf("sha256:%x", sum), nil
}

func canonicalRelativePath(value string, allowEmpty bool) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || value == "." {
		if allowEmpty {
			return "", nil
		}
		return "", errors.New("relative path is required")
	}
	if strings.ContainsAny(value, "\x00\\") || strings.HasPrefix(value, "/") {
		return "", errors.New("unsafe relative path")
	}
	for _, segment := range strings.Split(value, "/") {
		if segment == ".." {
			return "", errors.New("relative path escapes root")
		}
	}
	cleaned := path.Clean(value)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", errors.New("relative path escapes root")
	}
	return cleaned, nil
}

func dedupeStrings(values []string) []string {
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

type ArtifactRef struct {
	ArtifactID       string `json:"artifact_id"`
	Repository       string `json:"repository"`
	Digest           string `json:"digest"`
	SBOMDigest       string `json:"sbom_digest"`
	ScanDigest       string `json:"scan_digest"`
	SignatureDigest  string `json:"signature_digest"`
	ProvenanceDigest string `json:"provenance_digest"`
}

func (r ArtifactRef) Validate() error {
	if strings.TrimSpace(r.ArtifactID) == "" || strings.TrimSpace(r.Repository) == "" || !digest.MatchString(r.Digest) || !digest.MatchString(r.SBOMDigest) || !digest.MatchString(r.ScanDigest) || !digest.MatchString(r.SignatureDigest) || !digest.MatchString(r.ProvenanceDigest) {
		return errors.New("artifact trust chain is incomplete")
	}
	return nil
}

type VerifiedBuildReceipt struct {
	SourceSHA   string    `json:"source_sha"`
	SpecDigest  string    `json:"spec_digest"`
	Repository  string    `json:"repository"`
	Digest      string    `json:"digest"`
	MediaType   string    `json:"media_type"`
	CapturedAt  time.Time `json:"captured_at"`
	Builder     string    `json:"builder"`
	BuilderAddr string    `json:"builder_addr,omitempty"`
}

func (r VerifiedBuildReceipt) Validate() error {
	sourceSHA := strings.ToLower(strings.TrimSpace(r.SourceSHA))
	if !fullSHA.MatchString(sourceSHA) || !digest.MatchString(strings.TrimSpace(r.SpecDigest)) ||
		strings.TrimSpace(r.Repository) == "" || strings.ContainsAny(r.Repository, "\x00\r\n\t @") ||
		!digest.MatchString(strings.TrimSpace(r.Digest)) || strings.TrimSpace(r.MediaType) == "" ||
		r.CapturedAt.IsZero() || strings.TrimSpace(r.Builder) != "rootless-buildkit" ||
		strings.ContainsAny(r.BuilderAddr, "\x00\r\n") {
		return errors.New("invalid verified build receipt")
	}
	return nil
}

type BuildView struct {
	BuildID     string       `json:"build_id"`
	ProjectID   string       `json:"project_id"`
	WorkspaceID string       `json:"workspace_id"`
	SpecDigest  string       `json:"spec_digest"`
	State       string       `json:"state"`
	Artifact    *ArtifactRef `json:"artifact,omitempty"`
	StartedAt   *time.Time   `json:"started_at,omitempty"`
	FinishedAt  *time.Time   `json:"finished_at,omitempty"`
}
