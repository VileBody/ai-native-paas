package domain

import "time"

type MergeRequestState string

const (
	MergeRequestOpen   MergeRequestState = "OPEN"
	MergeRequestMerged MergeRequestState = "MERGED"
	MergeRequestClosed MergeRequestState = "CLOSED"
)

type MergeRequest struct {
	RepositoryID string
	ProviderIID  int64
	SourceBranch string
	TargetBranch string
	HeadSHA      string
	State        MergeRequestState
	Version      int64
	UpdatedAt    time.Time
}

func (m *MergeRequest) Apply(action, state, headSHA string, now time.Time) error {
	switch state {
	case "opened", "open", "reopened":
		m.State = MergeRequestOpen
	case "merged":
		m.State = MergeRequestMerged
	case "closed":
		m.State = MergeRequestClosed
	default:
		switch action {
		case "open", "reopen", "update":
			m.State = MergeRequestOpen
		case "merge":
			m.State = MergeRequestMerged
		case "close":
			m.State = MergeRequestClosed
		default:
			return NewError(CodeInvalidArgument, "unknown merge request state")
		}
	}
	m.HeadSHA = headSHA
	m.Version++
	m.UpdatedAt = now.UTC()
	return nil
}
