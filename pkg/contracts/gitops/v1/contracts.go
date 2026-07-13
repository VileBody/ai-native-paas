// Package v1 defines generic GitOps and Argo CD runtime contracts.
package v1

import (
	"errors"
	"regexp"
	"strings"
	"time"
)

const APIVersion = "gitops.platform.example.com/v1"

var digest = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

type Renderer string

const (
	RendererHelm      Renderer = "helm"
	RendererKustomize Renderer = "kustomize"
	RendererPlain     Renderer = "plain"
)

type Target struct {
	Environment    string   `json:"environment"`
	Namespace      string   `json:"namespace"`
	Renderer       Renderer `json:"renderer"`
	Path           string   `json:"path"`
	RevisionSHA    string   `json:"revision_sha"`
	ArtifactDigest string   `json:"artifact_digest"`
}

func (t Target) Validate() error {
	if t.Environment == "" || t.Namespace == "" || t.Path == "" || len(t.RevisionSHA) < 40 || !digest.MatchString(t.ArtifactDigest) {
		return errors.New("invalid GitOps target")
	}
	switch t.Renderer {
	case RendererHelm, RendererKustomize, RendererPlain:
		return nil
	default:
		return errors.New("invalid GitOps renderer")
	}
}

type PolicyDecision struct {
	Allowed        bool     `json:"allowed"`
	PolicyVersion  string   `json:"policy_version"`
	Reasons        []string `json:"reasons,omitempty"`
	ManifestDigest string   `json:"manifest_digest"`
}

type DeploymentObservation struct {
	ApplicationID  string    `json:"application_id"`
	ProjectID      string    `json:"project_id"`
	Environment    string    `json:"environment"`
	GitRevisionSHA string    `json:"git_revision_sha"`
	SyncStatus     string    `json:"sync_status"`
	HealthStatus   string    `json:"health_status"`
	EndpointURLs   []string  `json:"endpoint_urls,omitempty"`
	ObservedAt     time.Time `json:"observed_at"`
}

func (o DeploymentObservation) Validate() error {
	if strings.TrimSpace(o.ApplicationID) == "" || strings.TrimSpace(o.ProjectID) == "" || strings.TrimSpace(o.Environment) == "" || len(o.GitRevisionSHA) < 40 || o.SyncStatus == "" || o.HealthStatus == "" || o.ObservedAt.IsZero() {
		return errors.New("invalid deployment observation")
	}
	return nil
}

type RollbackRequest struct {
	ApplicationID  string `json:"application_id"`
	TargetRevision string `json:"target_revision"`
	BaseRevision   string `json:"base_revision"`
	Reason         string `json:"reason"`
}
