// Package v2 defines explicit, immutable and verifiable build contracts.
package v2

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

const APIVersion = "build.platform.example.com/v2"

var digest = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

type Driver string

const (
	DriverDockerfile Driver = "dockerfile"
	DriverBuildpacks Driver = "buildpacks"
	DriverNix        Driver = "nix"
	DriverCustom     Driver = "custom-approved"
)

type BuildSpec struct {
	SourceSHA      string            `json:"source_sha"`
	SourceRoot     string            `json:"source_root,omitempty"`
	Driver         Driver            `json:"driver"`
	DefinitionPath string            `json:"definition_path,omitempty"`
	Platforms      []string          `json:"platforms"`
	BuildArguments map[string]string `json:"build_arguments,omitempty"`
	SecretRefs     []string          `json:"secret_refs,omitempty"`
	NetworkPolicy  string            `json:"network_policy"`
	CacheScope     string            `json:"cache_scope"`
	TimeoutSeconds int64             `json:"timeout_seconds"`
}

func (s BuildSpec) Validate() error {
	switch s.Driver {
	case DriverDockerfile, DriverBuildpacks, DriverNix, DriverCustom:
	default:
		return errors.New("invalid build driver")
	}
	if len(s.SourceSHA) < 40 || len(s.SourceSHA) > 64 || len(s.Platforms) == 0 || len(s.Platforms) > 8 || s.NetworkPolicy != "governed" || s.CacheScope == "" || s.TimeoutSeconds < 60 || s.TimeoutSeconds > 86400 {
		return errors.New("invalid build specification")
	}
	if s.Driver == DriverDockerfile && s.DefinitionPath == "" {
		return errors.New("Dockerfile path is required")
	}
	for _, platform := range s.Platforms {
		if platform != "linux/amd64" && platform != "linux/arm64" {
			return errors.New("unsupported build platform")
		}
	}
	return nil
}

func (s BuildSpec) Fingerprint() (string, error) {
	if err := s.Validate(); err != nil {
		return "", err
	}
	raw, err := json.Marshal(s)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return fmt.Sprintf("sha256:%x", sum), nil
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
