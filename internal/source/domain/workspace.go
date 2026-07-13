package domain

import "time"

type WorkspaceState string

const (
	WorkspaceCreated   WorkspaceState = "CREATED"
	WorkspaceCloned    WorkspaceState = "CLONED"
	WorkspacePatched   WorkspaceState = "PATCHED"
	WorkspaceCommitted WorkspaceState = "COMMITTED"
	WorkspacePushed    WorkspaceState = "PUSHED"
	WorkspaceCompleted WorkspaceState = "COMPLETED"
	WorkspaceFailed    WorkspaceState = "FAILED"
	WorkspaceExpired   WorkspaceState = "EXPIRED"
	WorkspaceDeleted   WorkspaceState = "DELETED"
)

type Workspace struct {
	ID           string
	TenantID     string
	RepositoryID string
	Branch       string
	BaseSHA      string
	CommitSHA    string
	Directory    string
	CredentialID string
	State        WorkspaceState
	LastError    string
	ExpiresAt    time.Time
	Version      int64
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

func NewWorkspace(id, tenantID, repositoryID, branch, baseSHA string, now, expiresAt time.Time) (Workspace, error) {
	if id == "" || tenantID == "" || repositoryID == "" || branch == "" || baseSHA == "" || !expiresAt.After(now) {
		return Workspace{}, NewError(CodeInvalidArgument, "invalid workspace")
	}
	return Workspace{ID: id, TenantID: tenantID, RepositoryID: repositoryID, Branch: branch, BaseSHA: baseSHA, State: WorkspaceCreated, ExpiresAt: expiresAt.UTC(), Version: 1, CreatedAt: now.UTC(), UpdatedAt: now.UTC()}, nil
}
func (w *Workspace) transition(from []WorkspaceState, to WorkspaceState, now time.Time) error {
	ok := false
	for _, s := range from {
		if w.State == s {
			ok = true
			break
		}
	}
	if !ok {
		return NewError(CodeConflict, "invalid workspace transition")
	}
	w.State = to
	w.Version++
	w.UpdatedAt = now.UTC()
	w.LastError = ""
	return nil
}
func (w *Workspace) MarkCloned(dir, credentialID string, now time.Time) error {
	w.Directory = dir
	w.CredentialID = credentialID
	return w.transition([]WorkspaceState{WorkspaceCreated}, WorkspaceCloned, now)
}
func (w *Workspace) MarkPatched(now time.Time) error {
	return w.transition([]WorkspaceState{WorkspaceCloned, WorkspacePatched}, WorkspacePatched, now)
}
func (w *Workspace) MarkCommitted(sha string, now time.Time) error {
	w.CommitSHA = sha
	return w.transition([]WorkspaceState{WorkspacePatched, WorkspaceCommitted}, WorkspaceCommitted, now)
}
func (w *Workspace) MarkPushed(now time.Time) error {
	return w.transition([]WorkspaceState{WorkspaceCommitted, WorkspacePushed}, WorkspacePushed, now)
}
func (w *Workspace) MarkCompleted(now time.Time) error {
	w.CredentialID = ""
	return w.transition([]WorkspaceState{WorkspacePushed}, WorkspaceCompleted, now)
}
func (w *Workspace) MarkFailed(err error, now time.Time) {
	w.State = WorkspaceFailed
	w.LastError = truncate(err.Error(), 1024)
	w.Version++
	w.UpdatedAt = now.UTC()
}
func (w *Workspace) MarkExpired(now time.Time) error {
	if now.Before(w.ExpiresAt) {
		return NewError(CodeConflict, "workspace has not expired")
	}
	if w.State == WorkspaceDeleted {
		return nil
	}
	w.State = WorkspaceExpired
	w.Version++
	w.UpdatedAt = now.UTC()
	return nil
}
func (w *Workspace) MarkDeleted(now time.Time) error {
	if w.State != WorkspaceCompleted && w.State != WorkspaceExpired && w.State != WorkspaceFailed && w.State != WorkspaceCreated {
		return NewError(CodeConflict, "workspace cannot be deleted")
	}
	w.State = WorkspaceDeleted
	w.Directory = ""
	w.CredentialID = ""
	w.Version++
	w.UpdatedAt = now.UTC()
	return nil
}
