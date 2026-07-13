// Package v2 defines immutable Git source and attestation contracts.
package v2

import (
	"errors"
	"path"
	"regexp"
	"strings"
	"time"
)

const APIVersion = "source.platform.example.com/v2"

var (
	fullSHA = regexp.MustCompile(`^[0-9a-f]{40,64}$`)
	digest  = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
)

type RepositoryRef struct {
	RepositoryID  string `json:"repository_id"`
	ProjectID     string `json:"project_id"`
	Provider      string `json:"provider"`
	CloneURL      string `json:"clone_url"`
	WebURL        string `json:"web_url"`
	DefaultBranch string `json:"default_branch"`
}

type SourceRevision struct {
	RepositoryID string `json:"repository_id"`
	CommitSHA    string `json:"commit_sha"`
	SourceRoot   string `json:"source_root,omitempty"`
}

func (r SourceRevision) Validate() error {
	if strings.TrimSpace(r.RepositoryID) == "" || !fullSHA.MatchString(r.CommitSHA) || !safePath(r.SourceRoot) {
		return errors.New("invalid immutable source revision")
	}
	return nil
}

func safePath(value string) bool {
	if value == "" {
		return true
	}
	return !strings.HasPrefix(value, "/") && !strings.Contains(value, `\`) && path.Clean(value) == value && value != ".." && !strings.HasPrefix(value, "../")
}

type PatchFile struct {
	Path        string `json:"path"`
	ContentHash string `json:"content_hash"`
	Delete      bool   `json:"delete,omitempty"`
}

type ChangeSet struct {
	RepositoryID string      `json:"repository_id"`
	BaseSHA      string      `json:"base_sha"`
	TargetBranch string      `json:"target_branch"`
	Files        []PatchFile `json:"files"`
}

type CommitAttestation struct {
	RepositoryID    string    `json:"repository_id"`
	CommitSHA       string    `json:"commit_sha"`
	AgentID         string    `json:"agent_id"`
	TaskID          string    `json:"task_id"`
	CorrelationID   string    `json:"correlation_id"`
	StatementDigest string    `json:"statement_digest"`
	SignatureDigest string    `json:"signature_digest"`
	IssuedAt        time.Time `json:"issued_at"`
}

func (a CommitAttestation) Validate() error {
	if !fullSHA.MatchString(a.CommitSHA) || !digest.MatchString(a.StatementDigest) || !digest.MatchString(a.SignatureDigest) || a.AgentID == "" || a.TaskID == "" || a.CorrelationID == "" || a.IssuedAt.IsZero() {
		return errors.New("invalid commit attestation")
	}
	return nil
}

type ProtectedPathPolicy struct {
	Patterns                 []string `json:"patterns"`
	RequireMergeRequest      bool     `json:"require_merge_request"`
	RequireHumanApproval     bool     `json:"require_human_approval"`
	RequireSignedAgentCommit bool     `json:"require_signed_agent_commit"`
}
