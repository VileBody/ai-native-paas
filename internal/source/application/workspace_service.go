package application

import (
	"context"
	"encoding/json"
	"time"

	"github.com/keir-research/ai-native-paas/internal/source/domain"
)

type WorkspaceService struct {
	Store    Store
	Provider GitProvider
	Git      WorkspaceGit
	Clock    Clock
	IDs      IDGenerator
}
type CreateWorkspaceCommand struct {
	TenantID, ActorID, RepositoryID, Branch, BaseSHA string
	TTL                                              time.Duration
}

func (s *WorkspaceService) Create(ctx context.Context, cmd CreateWorkspaceCommand) (domain.Workspace, error) {
	if cmd.TTL <= 0 {
		cmd.TTL = 30 * time.Minute
	}
	now := s.Clock.Now()
	w, err := domain.NewWorkspace(s.IDs.NewID("ws"), cmd.TenantID, cmd.RepositoryID, cmd.Branch, cmd.BaseSHA, now, now.Add(cmd.TTL))
	if err != nil {
		return domain.Workspace{}, err
	}
	err = s.Store.Transact(ctx, func(tx Tx) error {
		r, ok := tx.GetRepository(cmd.RepositoryID)
		if !ok || r.TenantID != cmd.TenantID || r.State != domain.RepositoryReady {
			return domain.NewError(domain.CodeNotFound, "ready repository not found")
		}
		if err := tx.InsertWorkspace(w); err != nil {
			return err
		}
		return tx.AppendAudit(AuditRecord{ID: s.IDs.NewID("aud"), TenantID: cmd.TenantID, ActorID: cmd.ActorID, Action: "source.workspace.create", ResourceType: "workspace", ResourceID: w.ID, Data: []byte(`{}`), CreatedAt: now})
	})
	return w, err
}

type ExecuteWorkspaceCommand struct {
	TenantID, ActorID, WorkspaceID, RemoteURL, Message, AuthorName, AuthorEmail string
	Patch                                                                       []PatchOperation
}

func (s *WorkspaceService) Execute(ctx context.Context, cmd ExecuteWorkspaceCommand) (domain.Workspace, error) {
	w, repo, err := s.load(ctx, cmd.TenantID, cmd.WorkspaceID)
	if err != nil {
		return domain.Workspace{}, err
	}
	if w.State == domain.WorkspaceCompleted {
		return w, nil
	}
	if !s.Clock.Now().Before(w.ExpiresAt) && w.State != domain.WorkspacePushed {
		_ = s.expire(ctx, &w)
		return w, domain.NewError(domain.CodeConflict, "workspace expired")
	}
	var credential ProviderCredential
	if w.State == domain.WorkspaceCreated {
		credential, err = s.Provider.CreateCredential(ctx, repo.ProviderProjectID, "workspace-"+w.ID, w.ExpiresAt)
		if err != nil {
			return w, domain.Wrap(domain.CodeExternal, "credential creation failed", err)
		}
		dir, cloneErr := s.Git.CloneExact(ctx, cmd.RemoteURL, w.BaseSHA, w.Branch, credential)
		if cloneErr != nil {
			_ = s.Provider.RevokeCredential(ctx, repo.ProviderProjectID, credential.ID)
			w.MarkFailed(cloneErr, s.Clock.Now())
			_ = s.save(ctx, w, w.Version-1)
			return w, cloneErr
		}
		old := w.Version
		if err = w.MarkCloned(dir, credential.ID, s.Clock.Now()); err != nil {
			return w, err
		}
		if err = s.save(ctx, w, old); err != nil {
			return w, err
		}
	} else if w.CredentialID != "" {
		credential = ProviderCredential{ID: w.CredentialID}
	}
	// A retry after a failed revoke starts here and never pushes again.
	if w.State == domain.WorkspacePushed {
		return s.finishCredential(ctx, w, repo, credential)
	}
	if credential.Token == "" {
		// Credentials are intentionally not persisted. A resumed pre-push workspace gets a fresh token.
		credential, err = s.Provider.CreateCredential(ctx, repo.ProviderProjectID, "workspace-"+w.ID, w.ExpiresAt)
		if err != nil {
			return w, err
		}
		w.CredentialID = credential.ID
	}
	if w.State == domain.WorkspaceCloned {
		if err = s.Git.Apply(ctx, w.Directory, cmd.Patch); err != nil {
			return s.fail(ctx, w, err)
		}
		old := w.Version
		if err = w.MarkPatched(s.Clock.Now()); err != nil {
			return w, err
		}
		if err = s.save(ctx, w, old); err != nil {
			return w, err
		}
	}
	if w.State == domain.WorkspacePatched {
		sha, commitErr := s.Git.Commit(ctx, w.Directory, cmd.Message, cmd.AuthorName, cmd.AuthorEmail)
		if commitErr != nil {
			return s.fail(ctx, w, commitErr)
		}
		old := w.Version
		if err = w.MarkCommitted(sha, s.Clock.Now()); err != nil {
			return w, err
		}
		if err = s.save(ctx, w, old); err != nil {
			return w, err
		}
	}
	if w.State == domain.WorkspaceCommitted {
		pushed, pushErr := s.Git.Push(ctx, w.Directory, w.Branch, w.BaseSHA, w.CommitSHA, credential)
		if pushErr != nil || !pushed {
			if pushErr == nil {
				pushErr = domain.NewError(domain.CodeExternal, "git push not confirmed")
			}
			return s.fail(ctx, w, pushErr)
		}
		old := w.Version
		if err = w.MarkPushed(s.Clock.Now()); err != nil {
			return w, err
		}
		if err = s.save(ctx, w, old); err != nil {
			return w, err
		}
	}
	return s.finishCredential(ctx, w, repo, credential)
}
func (s *WorkspaceService) finishCredential(ctx context.Context, w domain.Workspace, repo domain.Repository, credential ProviderCredential) (domain.Workspace, error) {
	id := w.CredentialID
	if credential.ID != "" {
		id = credential.ID
	}
	if id != "" {
		if err := s.Provider.RevokeCredential(ctx, repo.ProviderProjectID, id); err != nil {
			return w, domain.Wrap(domain.CodeExternal, "credential revocation failed", err)
		}
	}
	old := w.Version
	if err := w.MarkCompleted(s.Clock.Now()); err != nil {
		return w, err
	}
	if err := s.save(ctx, w, old); err != nil {
		return w, err
	}
	_ = s.Git.Cleanup(ctx, w.Directory)
	payload, _ := json.Marshal(map[string]any{"workspace_id": w.ID, "repository_id": w.RepositoryID, "commit_sha": w.CommitSHA, "branch": w.Branch})
	_ = s.Store.Transact(ctx, func(tx Tx) error {
		return tx.AppendOutbox(OutboxRecord{ID: s.IDs.NewID("evt"), Topic: "source.workspace_pushed.v1", AggregateID: w.RepositoryID, Payload: payload, CreatedAt: s.Clock.Now()})
	})
	return w, nil
}
func (s *WorkspaceService) Delete(ctx context.Context, tenantID, workspaceID string) (domain.Workspace, error) {
	w, _, err := s.load(ctx, tenantID, workspaceID)
	if err != nil {
		return w, err
	}
	expectedVersion := w.Version
	if !s.Clock.Now().Before(w.ExpiresAt) && w.State != domain.WorkspaceCompleted && w.State != domain.WorkspaceFailed {
		_ = w.MarkExpired(s.Clock.Now())
	}
	if err = s.Git.Cleanup(ctx, w.Directory); err != nil {
		return w, err
	}
	if err = w.MarkDeleted(s.Clock.Now()); err != nil {
		return w, err
	}
	return w, s.save(ctx, w, expectedVersion)
}
func (s *WorkspaceService) load(ctx context.Context, tenant, id string) (domain.Workspace, domain.Repository, error) {
	var w domain.Workspace
	var r domain.Repository
	err := s.Store.Transact(ctx, func(tx Tx) error {
		var ok bool
		w, ok = tx.GetWorkspace(id)
		if !ok || w.TenantID != tenant {
			return domain.NewError(domain.CodeNotFound, "workspace not found")
		}
		r, ok = tx.GetRepository(w.RepositoryID)
		if !ok {
			return domain.NewError(domain.CodeNotFound, "repository not found")
		}
		return nil
	})
	return w, r, err
}
func (s *WorkspaceService) save(ctx context.Context, w domain.Workspace, expected int64) error {
	return s.Store.Transact(ctx, func(tx Tx) error { return tx.UpdateWorkspace(w, expected) })
}
func (s *WorkspaceService) fail(ctx context.Context, w domain.Workspace, err error) (domain.Workspace, error) {
	old := w.Version
	w.MarkFailed(err, s.Clock.Now())
	_ = s.save(ctx, w, old)
	return w, err
}
func (s *WorkspaceService) expire(ctx context.Context, w *domain.Workspace) error {
	old := w.Version
	if err := w.MarkExpired(s.Clock.Now()); err != nil {
		return err
	}
	return s.save(ctx, *w, old)
}
