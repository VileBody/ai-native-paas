package v1

import (
	"errors"
	"strings"
	"time"
)

var ErrInvalidSourceRevision = errors.New("invalid source revision")

type SourceRevision struct {
	ProjectID    string `json:"project_id"`
	RepositoryID string `json:"repository_id"`
	Branch       string `json:"branch"`
	CommitSHA    string `json:"commit_sha"`
	SourceRoot   string `json:"source_root,omitempty"`
}

func (r SourceRevision) Validate() error {
	if strings.TrimSpace(r.ProjectID) == "" || strings.TrimSpace(r.RepositoryID) == "" ||
		strings.TrimSpace(r.Branch) == "" || !ValidCommitSHA(r.CommitSHA) {
		return ErrInvalidSourceRevision
	}
	if r.SourceRoot == "." {
		r.SourceRoot = ""
	}
	if strings.HasPrefix(r.SourceRoot, "/") || strings.Contains(r.SourceRoot, "..") {
		return ErrInvalidSourceRevision
	}
	return nil
}

func ValidCommitSHA(v string) bool {
	if len(v) < 7 || len(v) > 64 {
		return false
	}
	for _, c := range v {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') && (c < 'A' || c > 'F') {
			return false
		}
	}
	return true
}

type RepositoryRef struct {
	RepositoryID      string `json:"repository_id"`
	ProjectID         string `json:"project_id"`
	Provider          string `json:"provider"`
	ProviderProjectID int64  `json:"provider_project_id"`
	WebURL            string `json:"web_url"`
	DefaultBranch     string `json:"default_branch"`
}

type BranchRevision struct {
	RepositoryID string    `json:"repository_id"`
	Branch       string    `json:"branch"`
	CommitSHA    string    `json:"commit_sha"`
	ObservedAt   time.Time `json:"observed_at"`
}

type EventKind string

const (
	EventPush            EventKind = "source.push.v1"
	EventMergeRequest    EventKind = "source.merge_request.v1"
	EventRepositoryReady EventKind = "source.repository_ready.v1"
)

type SourceEvent struct {
	EventID           string    `json:"event_id"`
	Kind              EventKind `json:"kind"`
	TenantID          string    `json:"tenant_id"`
	Provider          string    `json:"provider"`
	ProviderProjectID int64     `json:"provider_project_id"`
	RepositoryID      string    `json:"repository_id,omitempty"`
	Branch            string    `json:"branch,omitempty"`
	BeforeSHA         string    `json:"before_sha,omitempty"`
	AfterSHA          string    `json:"after_sha,omitempty"`
	MergeRequestIID   int64     `json:"merge_request_iid,omitempty"`
	Action            string    `json:"action,omitempty"`
	OccurredAt        time.Time `json:"occurred_at"`
}
