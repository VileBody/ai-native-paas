package v1

import (
	"context"
	"errors"
	"strings"
	"time"
)

var ErrInvalidArtifactRef = errors.New("invalid artifact reference")

type ArtifactRef struct {
	ArtifactID string `json:"artifact_id"`
	Repository string `json:"repository"`
	Digest     string `json:"digest"`
	MediaType  string `json:"media_type"`
}

func (r ArtifactRef) Validate() error {
	if strings.TrimSpace(r.ArtifactID) == "" || strings.TrimSpace(r.Repository) == "" ||
		!ValidDigest(r.Digest) || strings.TrimSpace(r.MediaType) == "" {
		return ErrInvalidArtifactRef
	}
	return nil
}

func ValidDigest(v string) bool {
	if !strings.HasPrefix(v, "sha256:") || len(v) != len("sha256:")+64 {
		return false
	}
	for _, c := range v[len("sha256:"):] {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

type BuildState string

const (
	BuildQueued         BuildState = "QUEUED"
	BuildFetchingSource BuildState = "FETCHING_SOURCE"
	BuildDetecting      BuildState = "DETECTING"
	BuildBuilding       BuildState = "BUILDING"
	BuildExporting      BuildState = "EXPORTING"
	BuildScanning       BuildState = "SCANNING"
	BuildSigning        BuildState = "SIGNING"
	BuildSucceeded      BuildState = "SUCCEEDED"
	BuildFailedUserCode BuildState = "FAILED_USER_CODE"
	BuildFailedPlatform BuildState = "FAILED_PLATFORM"
	BuildCanceled       BuildState = "CANCELED"
	BuildSuperseded     BuildState = "SUPERSEDED"
	BuildTimedOut       BuildState = "TIMED_OUT"
)

type BuildView struct {
	BuildID       string       `json:"build_id"`
	TenantID      string       `json:"tenant_id"`
	Identity      string       `json:"identity"`
	State         BuildState   `json:"state"`
	CorrelationID string       `json:"correlation_id"`
	Artifact      *ArtifactRef `json:"artifact,omitempty"`
	FailureCode   string       `json:"failure_code,omitempty"`
	Retryable     bool         `json:"retryable,omitempty"`
	CreatedAt     time.Time    `json:"created_at"`
	UpdatedAt     time.Time    `json:"updated_at"`
}

type ReleasabilityDecision struct {
	Allowed       bool     `json:"allowed"`
	PolicyVersion string   `json:"policy_version"`
	Reasons       []string `json:"reasons,omitempty"`
}

type ArtifactPolicy interface {
	IsReleasable(context.Context, ArtifactRef) (ReleasabilityDecision, error)
}
