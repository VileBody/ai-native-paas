package domain

import (
	"strings"
	"time"
)

const (
	QuarantineExternalIdentityMismatch = "external_identity_mismatch"
	QuarantineManagedLabelMissing      = "managed_label_missing"
	QuarantineUnboundManagedProject    = "unbound_managed_project"
)

// ProviderQuarantine is platform inventory, not a tenant resource binding.
// CandidateRepositoryID explains the path collision but never authorizes
// adoption of the provider project.
type ProviderQuarantine struct {
	Provider                string
	ProviderNamespaceID     int64
	ProviderProjectID       int64
	ProviderPath            string
	WebURL                  string
	CandidateRepositoryID   string
	Reason                  string
	ExternalIdentityMatched bool
	ManagedLabelPresent     bool
	FirstObservedAt         time.Time
	LastObservedAt          time.Time
	Version                 int64
}

func NewProviderQuarantine(provider string, namespaceID, projectID int64, path, webURL, candidateRepositoryID, reason string, externalIdentityMatched, managedLabelPresent bool, now time.Time) (ProviderQuarantine, error) {
	provider = strings.TrimSpace(provider)
	path = strings.TrimSpace(path)
	candidateRepositoryID = strings.TrimSpace(candidateRepositoryID)
	if provider == "" || namespaceID <= 0 || projectID <= 0 || path == "" || len(path) > 512 || len(webURL) > 2048 || candidateRepositoryID == "" || !validQuarantineReason(reason) || now.IsZero() {
		return ProviderQuarantine{}, NewError(CodeInvalidArgument, "invalid provider quarantine")
	}
	now = now.UTC()
	return ProviderQuarantine{
		Provider: provider, ProviderNamespaceID: namespaceID, ProviderProjectID: projectID,
		ProviderPath: path, WebURL: webURL, CandidateRepositoryID: candidateRepositoryID, Reason: reason,
		ExternalIdentityMatched: externalIdentityMatched, ManagedLabelPresent: managedLabelPresent,
		FirstObservedAt: now, LastObservedAt: now, Version: 1,
	}, nil
}

func (q *ProviderQuarantine) Observe(path, webURL, candidateRepositoryID, reason string, externalIdentityMatched, managedLabelPresent bool, now time.Time) (bool, error) {
	path = strings.TrimSpace(path)
	candidateRepositoryID = strings.TrimSpace(candidateRepositoryID)
	if path == "" || len(path) > 512 || len(webURL) > 2048 || candidateRepositoryID == "" || !validQuarantineReason(reason) || now.IsZero() {
		return false, NewError(CodeInvalidArgument, "invalid provider quarantine observation")
	}
	if now.UTC().Before(q.FirstObservedAt) {
		return false, NewError(CodeConflict, "provider quarantine observation predates first observation")
	}
	materialChanged := q.ProviderPath != path || q.WebURL != webURL || q.CandidateRepositoryID != candidateRepositoryID || q.Reason != reason || q.ExternalIdentityMatched != externalIdentityMatched || q.ManagedLabelPresent != managedLabelPresent
	q.ProviderPath = path
	q.WebURL = webURL
	q.CandidateRepositoryID = candidateRepositoryID
	q.Reason = reason
	q.ExternalIdentityMatched = externalIdentityMatched
	q.ManagedLabelPresent = managedLabelPresent
	q.LastObservedAt = now.UTC()
	q.Version++
	return materialChanged, nil
}

func validQuarantineReason(reason string) bool {
	return reason == QuarantineExternalIdentityMismatch || reason == QuarantineManagedLabelMissing || reason == QuarantineUnboundManagedProject
}
