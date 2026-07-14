package domain

import (
	"strings"
	"time"
)

type BranchHead struct {
	RepositoryID  string
	Name          string
	CommitSHA     string
	EnvironmentID string
	LastEventID   string
	LastEventAt   time.Time
	ObservedAt    time.Time
	DeletedAt     time.Time
	Version       int64
}

type AuthoritativePushResult struct {
	HeadChanged  bool
	StateChanged bool
	Stale        bool
}

func NewBranchHead(repositoryID, name string) (BranchHead, error) {
	if strings.TrimSpace(repositoryID) == "" || strings.TrimSpace(name) == "" {
		return BranchHead{}, NewError(CodeInvalidArgument, "repository and branch are required")
	}
	return BranchHead{RepositoryID: repositoryID, Name: name, Version: 1}, nil
}

// ApplyPush accepts a push only when it extends the locally observed chain.
// A gap or out-of-order event is reported as stale so the caller can query the provider's authoritative head.
func (b *BranchHead) ApplyPush(before, after, eventID string, eventAt, now time.Time) (bool, error) {
	if strings.TrimSpace(after) == "" || strings.TrimSpace(eventID) == "" {
		return false, NewError(CodeInvalidArgument, "after sha and event id are required")
	}
	eventAt = eventAt.UTC()
	if eventID == b.LastEventID || after == b.CommitSHA {
		return false, nil
	}
	if !b.LastEventAt.IsZero() && eventAt.Before(b.LastEventAt) {
		return false, NewError(CodeStaleVersion, "out-of-order push event")
	}
	if b.CommitSHA != "" && before != "" && before != b.CommitSHA {
		return false, NewError(CodeStaleVersion, "push does not extend observed branch head")
	}
	b.CommitSHA = after
	b.LastEventID = eventID
	b.LastEventAt = eventAt
	b.ObservedAt = now.UTC()
	b.Version++
	return true, nil
}
func (b *BranchHead) SetAuthoritative(sha string, observedAt time.Time) (bool, error) {
	if strings.TrimSpace(sha) == "" {
		return false, NewError(CodeInvalidArgument, "commit sha required")
	}
	if b.CommitSHA == sha {
		b.ObservedAt = observedAt.UTC()
		return false, nil
	}
	b.CommitSHA = sha
	b.ObservedAt = observedAt.UTC()
	b.Version++
	return true, nil
}

// ObserveAuthoritativePush records provider-authoritative state while retaining
// webhook ordering metadata. An older delivery is persisted in the webhook
// inbox by the application layer but cannot regress the branch head.
func (b *BranchHead) ObserveAuthoritativePush(sha, eventID string, eventAt, observedAt time.Time) (AuthoritativePushResult, error) {
	if strings.TrimSpace(sha) == "" || strings.TrimSpace(eventID) == "" || eventAt.IsZero() {
		return AuthoritativePushResult{}, NewError(CodeInvalidArgument, "commit sha, event id and event time are required")
	}
	eventAt = eventAt.UTC()
	if eventID == b.LastEventID {
		return AuthoritativePushResult{}, nil
	}
	if !b.LastEventAt.IsZero() && eventAt.Before(b.LastEventAt) {
		return AuthoritativePushResult{Stale: true}, nil
	}
	headChanged := b.CommitSHA != sha || !b.DeletedAt.IsZero()
	b.CommitSHA = sha
	b.LastEventID = eventID
	b.LastEventAt = eventAt
	b.ObservedAt = observedAt.UTC()
	b.DeletedAt = time.Time{}
	b.Version++
	return AuthoritativePushResult{HeadChanged: headChanged, StateChanged: true}, nil
}

func (b *BranchHead) BindPreviewEnvironment(environmentID string) (bool, error) {
	environmentID = strings.TrimSpace(environmentID)
	if environmentID == "" {
		return false, NewError(CodeInvalidArgument, "environment id is required")
	}
	if b.EnvironmentID == environmentID {
		return false, nil
	}
	if b.EnvironmentID != "" {
		return false, NewError(CodeConflict, "branch is already bound to another environment")
	}
	b.EnvironmentID = environmentID
	b.Version++
	return true, nil
}

// ObserveDeletion retains the last immutable revision and the environment
// binding. This makes cleanup delivery retryable without deleting runtime state
// from the source-control transaction.
func (b *BranchHead) ObserveDeletion(eventID string, eventAt, observedAt time.Time) (AuthoritativePushResult, error) {
	if strings.TrimSpace(eventID) == "" || eventAt.IsZero() {
		return AuthoritativePushResult{}, NewError(CodeInvalidArgument, "event id and event time are required")
	}
	eventAt = eventAt.UTC()
	if eventID == b.LastEventID {
		return AuthoritativePushResult{}, nil
	}
	if !b.LastEventAt.IsZero() && eventAt.Before(b.LastEventAt) {
		return AuthoritativePushResult{Stale: true}, nil
	}
	if !b.DeletedAt.IsZero() {
		return AuthoritativePushResult{}, nil
	}
	b.LastEventID = eventID
	b.LastEventAt = eventAt
	b.ObservedAt = observedAt.UTC()
	b.DeletedAt = observedAt.UTC()
	b.Version++
	return AuthoritativePushResult{StateChanged: true}, nil
}
