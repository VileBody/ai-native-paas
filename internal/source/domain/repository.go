package domain

import (
	"strings"
	"time"
)

type RepositoryState string

const (
	RepositoryRequested    RepositoryState = "REQUESTED"
	RepositoryProvisioning RepositoryState = "PROVISIONING"
	RepositoryReady        RepositoryState = "READY"
	RepositoryFailed       RepositoryState = "FAILED"
	RepositorySuspended    RepositoryState = "SUSPENDED"
)

type Repository struct {
	ID                  string
	TenantID            string
	ProjectID           string
	Provider            string
	ProviderNamespaceID int64
	ProviderProjectID   int64
	ProviderPath        string
	WebURL              string
	DefaultBranch       string
	State               RepositoryState
	LastError           string
	CorrelationID       string
	Version             int64
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

func NewRepository(id, tenantID, projectID, provider, correlationID string, namespaceID int64, now time.Time) (Repository, error) {
	if strings.TrimSpace(id) == "" || strings.TrimSpace(tenantID) == "" || strings.TrimSpace(projectID) == "" ||
		strings.TrimSpace(provider) == "" || strings.TrimSpace(correlationID) == "" || namespaceID <= 0 {
		return Repository{}, NewError(CodeInvalidArgument, "invalid repository request")
	}
	now = now.UTC()
	return Repository{ID: id, TenantID: tenantID, ProjectID: projectID, Provider: provider, ProviderNamespaceID: namespaceID,
		CorrelationID: correlationID, State: RepositoryRequested, DefaultBranch: "main", Version: 1, CreatedAt: now, UpdatedAt: now}, nil
}
func (r *Repository) BeginProvisioning(now time.Time) error {
	if r.State == RepositoryReady {
		return nil
	}
	if r.State != RepositoryRequested && r.State != RepositoryFailed && r.State != RepositoryProvisioning {
		return NewError(CodeConflict, "repository cannot enter provisioning")
	}
	if r.State != RepositoryProvisioning {
		r.State = RepositoryProvisioning
		r.Version++
	}
	r.LastError = ""
	r.UpdatedAt = now.UTC()
	return nil
}
func (r *Repository) AttachProvider(projectID int64, path, webURL, defaultBranch string, now time.Time) error {
	if projectID <= 0 || strings.TrimSpace(path) == "" {
		return NewError(CodeInvalidArgument, "provider project id and path are required")
	}
	if r.ProviderProjectID != 0 && r.ProviderProjectID != projectID {
		return NewError(CodeConflict, "provider identity is immutable")
	}
	r.ProviderProjectID = projectID
	r.ProviderPath = path
	r.WebURL = webURL
	if defaultBranch != "" {
		r.DefaultBranch = defaultBranch
	}
	r.State = RepositoryReady
	r.LastError = ""
	r.Version++
	r.UpdatedAt = now.UTC()
	return nil
}
func (r *Repository) SyncProviderMetadata(projectID int64, path, webURL, defaultBranch string, now time.Time) error {
	if r.ProviderProjectID == 0 || projectID != r.ProviderProjectID {
		return NewError(CodeConflict, "provider identity mismatch")
	}
	changed := r.ProviderPath != path || r.WebURL != webURL || (defaultBranch != "" && r.DefaultBranch != defaultBranch)
	r.ProviderPath = path
	r.WebURL = webURL
	if defaultBranch != "" {
		r.DefaultBranch = defaultBranch
	}
	if changed {
		r.Version++
		r.UpdatedAt = now.UTC()
	}
	return nil
}
func (r *Repository) FailProvisioning(message string, now time.Time) {
	r.State = RepositoryFailed
	r.LastError = truncate(message, 1024)
	r.Version++
	r.UpdatedAt = now.UTC()
}
func truncate(v string, n int) string {
	if len(v) <= n {
		return v
	}
	return v[:n]
}
