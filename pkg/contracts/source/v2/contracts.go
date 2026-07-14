// Package v2 defines immutable Git source and attestation contracts.
package v2

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
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

// PatchMutation is the bounded, content-carrying form accepted by the
// repository_apply_patch MCP tool. Content is base64 so the command envelope
// remains unambiguous and does not rely on a shell or stdin framing.
type PatchMutation struct {
	Path          string `json:"path"`
	ContentBase64 string `json:"content_base64,omitempty"`
	Delete        bool   `json:"delete,omitempty"`
}

func (p PatchMutation) Validate() error {
	if !safePath(p.Path) || p.Path == "" || p.Path == ".git" || strings.HasPrefix(p.Path, ".git/") {
		return errors.New("invalid patch path")
	}
	if p.Delete {
		if p.ContentBase64 != "" {
			return errors.New("deleted patch file carries content")
		}
		return nil
	}
	content, err := base64.StdEncoding.Strict().DecodeString(p.ContentBase64)
	if err != nil || len(content) > 1<<20 {
		return errors.New("invalid patch content")
	}
	return nil
}

// CommitStatement is canonical JSON signed by the workspace's short-lived
// mTLS private key. The control plane verifies the signature against the
// certificate used for the receipt request before persisting it.
type CommitStatement struct {
	RepositoryID   string    `json:"repository_id"`
	BaseSHA        string    `json:"base_sha"`
	CommitSHA      string    `json:"commit_sha"`
	Branch         string    `json:"branch"`
	AgentID        string    `json:"agent_id"`
	TaskID         string    `json:"task_id"`
	CorrelationID  string    `json:"correlation_id"`
	SourcePlanHash string    `json:"source_plan_hash"`
	IssuedAt       time.Time `json:"issued_at"`
}

func (s CommitStatement) Canonical() ([]byte, error) {
	if strings.TrimSpace(s.RepositoryID) == "" || !fullSHA.MatchString(s.BaseSHA) || !fullSHA.MatchString(s.CommitSHA) || !safeBranch(s.Branch) || strings.TrimSpace(s.AgentID) == "" || strings.TrimSpace(s.TaskID) == "" || strings.TrimSpace(s.CorrelationID) == "" || !digest.MatchString(s.SourcePlanHash) || s.IssuedAt.IsZero() {
		return nil, errors.New("invalid commit statement")
	}
	return json.Marshal(s)
}

type AgentCommitReceipt struct {
	SessionID              string            `json:"session_id"`
	ExecutionSessionID     string            `json:"execution_session_id"`
	CommandID              string            `json:"command_id"`
	Statement              CommitStatement   `json:"statement"`
	Attestation            CommitAttestation `json:"attestation"`
	Signature              string            `json:"signature"`
	CertificateFingerprint string            `json:"certificate_fingerprint"`
}

func (r AgentCommitReceipt) Validate() error {
	statement, err := r.Statement.Canonical()
	if err != nil || r.Attestation.Validate() != nil || strings.TrimSpace(r.SessionID) == "" || r.ExecutionSessionID != r.SessionID || strings.TrimSpace(r.CommandID) == "" || !digest.MatchString(r.CertificateFingerprint) {
		return errors.New("invalid agent commit receipt")
	}
	if r.Attestation.RepositoryID != r.Statement.RepositoryID || r.Attestation.CommitSHA != r.Statement.CommitSHA || r.Attestation.AgentID != r.Statement.AgentID || r.Attestation.TaskID != r.Statement.TaskID || r.Attestation.CorrelationID != r.Statement.CorrelationID || !r.Attestation.IssuedAt.Equal(r.Statement.IssuedAt) {
		return errors.New("commit receipt statement mismatch")
	}
	statementHash := sha256.Sum256(statement)
	if r.Attestation.StatementDigest != "sha256:"+hex.EncodeToString(statementHash[:]) {
		return errors.New("commit receipt statement digest mismatch")
	}
	signature, err := base64.StdEncoding.Strict().DecodeString(r.Signature)
	if err != nil || len(signature) == 0 || len(signature) > 1024 {
		return errors.New("invalid commit receipt signature")
	}
	signatureHash := sha256.Sum256(signature)
	if r.Attestation.SignatureDigest != "sha256:"+hex.EncodeToString(signatureHash[:]) {
		return errors.New("commit receipt signature digest mismatch")
	}
	return nil
}

func safeBranch(value string) bool {
	if value == "" || len(value) > 128 || strings.HasPrefix(value, "-") || strings.HasPrefix(value, "/") || strings.HasSuffix(value, "/") || strings.Contains(value, "..") || strings.ContainsAny(value, "~^:?*[\\ ") || strings.Contains(value, "@{") {
		return false
	}
	for _, segment := range strings.Split(value, "/") {
		if segment == "" || segment == "." || strings.HasPrefix(segment, ".") || strings.HasSuffix(segment, ".") || strings.HasSuffix(segment, ".lock") {
			return false
		}
	}
	return true
}

func ValidBranch(value string) bool { return safeBranch(value) }
