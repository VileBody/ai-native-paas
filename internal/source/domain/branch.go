package domain

import (
	"strings"
	"time"
)

type BranchHead struct {
	RepositoryID string
	Name         string
	CommitSHA    string
	LastEventID  string
	LastEventAt  time.Time
	ObservedAt   time.Time
	Version      int64
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
