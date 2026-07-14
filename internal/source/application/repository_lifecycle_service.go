package application

import (
	"context"
	"encoding/json"
	"time"

	"github.com/keir-research/ai-native-paas/internal/source/domain"
)

const (
	archiveRepositoryCommand = "source.archive_repository.v2"
	restoreRepositoryCommand = "source.restore_repository.v2"
	purgeRepositoryCommand   = "source.purge_repository.v2"
)

type ArchiveRepositoryCommand struct {
	TenantID       string
	ActorID        string
	RepositoryID   string
	IdempotencyKey string
}

func (s *Service) ArchiveRepository(ctx context.Context, cmd ArchiveRepositoryCommand) (domain.Repository, error) {
	if s.Store == nil || s.Provider == nil || s.Clock == nil || s.IDs == nil {
		return domain.Repository{}, domain.NewError(domain.CodeUnavailable, "source service is not configured")
	}
	if !safeMRMetadata(cmd.TenantID) || !safeMRMetadata(cmd.ActorID) || !safeMRMetadata(cmd.RepositoryID) || !safeMRMetadata(cmd.IdempotencyKey) {
		return domain.Repository{}, domain.NewError(domain.CodeInvalidArgument, "invalid archive repository request")
	}
	requestHash := hashJSON(struct{ RepositoryID, ActorID string }{cmd.RepositoryID, cmd.ActorID})
	var repository domain.Repository
	var workspaces []domain.Workspace
	var replay domain.Repository
	err := s.Store.Transact(ctx, func(tx Tx) error {
		value, ok := tx.GetRepository(cmd.RepositoryID)
		if !ok || value.TenantID != cmd.TenantID {
			return domain.NewError(domain.CodeNotFound, "repository not found")
		}
		if record, ok := tx.GetIdempotency(cmd.TenantID, cmd.IdempotencyKey); ok {
			if record.Command != archiveRepositoryCommand || record.RequestHash != requestHash {
				return domain.NewError(domain.CodeConflict, "idempotency key was used for another request")
			}
			if record.Completed {
				return json.Unmarshal(record.Result, &replay)
			}
		} else if err := tx.PutIdempotency(IdempotencyRecord{TenantID: cmd.TenantID, Key: cmd.IdempotencyKey, Command: archiveRepositoryCommand, RequestHash: requestHash, CreatedAt: s.Clock.Now().UTC()}); err != nil {
			return err
		}
		if value.State != domain.RepositoryReady && value.State != domain.RepositorySuspended {
			return domain.NewError(domain.CodeConflict, "repository cannot be archived")
		}
		// Close the local source gate before the provider call. If the provider
		// response is lost, the command remains safely retryable while no new
		// workspace can start against a repository being archived.
		expected := value.Version
		if err := value.Suspend(s.Clock.Now()); err != nil {
			return err
		}
		if value.Version != expected {
			if err := tx.UpdateRepository(value, expected); err != nil {
				return err
			}
		}
		repository = value
		workspaces = tx.ListWorkspaces(value.ID)
		return nil
	})
	if err != nil || replay.ID != "" {
		return replay, err
	}
	remote, err := s.Provider.ArchiveRepository(ctx, repository.ProviderProjectID)
	if err != nil {
		return domain.Repository{}, domain.Wrap(domain.CodeExternal, "git repository archive failed", err)
	}
	if remote.ID != repository.ProviderProjectID || !remote.Archived {
		return domain.Repository{}, domain.NewError(domain.CodeConflict, "provider did not confirm repository archive")
	}
	revoked := map[string]struct{}{}
	for _, workspace := range workspaces {
		if workspace.CredentialID == "" {
			continue
		}
		if _, seen := revoked[workspace.CredentialID]; seen {
			continue
		}
		if err := s.Provider.RevokeCredential(ctx, repository.ProviderProjectID, workspace.CredentialID); err != nil {
			return domain.Repository{}, domain.Wrap(domain.CodeExternal, "workspace credential revocation during archive failed", err)
		}
		revoked[workspace.CredentialID] = struct{}{}
	}
	err = s.Store.Transact(ctx, func(tx Tx) error {
		current, ok := tx.GetRepository(repository.ID)
		if !ok || current.TenantID != cmd.TenantID || current.ProviderProjectID != repository.ProviderProjectID {
			return domain.NewError(domain.CodeConflict, "repository provider identity changed")
		}
		expected := current.Version
		if err := current.Suspend(s.Clock.Now()); err != nil {
			return err
		}
		if current.Version != expected {
			if err := tx.UpdateRepository(current, expected); err != nil {
				return err
			}
		}
		payload, marshalErr := json.Marshal(map[string]any{"repository_id": current.ID, "provider_project_id": current.ProviderProjectID, "revoked_workspace_credentials": len(revoked)})
		if marshalErr != nil {
			return marshalErr
		}
		if err := tx.AppendOutbox(OutboxRecord{ID: s.IDs.NewID("evt"), Topic: "source.repository_archived.v2", AggregateID: current.ID, Payload: payload, CreatedAt: s.Clock.Now()}); err != nil {
			return err
		}
		if err := tx.AppendAudit(AuditRecord{ID: s.IDs.NewID("aud"), TenantID: cmd.TenantID, ActorID: cmd.ActorID, Action: "source.repository.archive", ResourceType: "repository", ResourceID: current.ID, Data: payload, CreatedAt: s.Clock.Now()}); err != nil {
			return err
		}
		repository = current
		return completeRepositoryLifecycleIdempotency(tx, cmd.TenantID, cmd.IdempotencyKey, archiveRepositoryCommand, requestHash, current, s.Clock.Now())
	})
	return repository, err
}

type RestoreRepositoryCommand struct {
	TenantID       string
	ActorID        string
	RepositoryID   string
	IdempotencyKey string
}

func (s *Service) RestoreRepository(ctx context.Context, cmd RestoreRepositoryCommand) (domain.Repository, error) {
	if s.Store == nil || s.Provider == nil || s.Clock == nil || s.IDs == nil {
		return domain.Repository{}, domain.NewError(domain.CodeUnavailable, "source service is not configured")
	}
	if !safeMRMetadata(cmd.TenantID) || !safeMRMetadata(cmd.ActorID) || !safeMRMetadata(cmd.RepositoryID) || !safeMRMetadata(cmd.IdempotencyKey) {
		return domain.Repository{}, domain.NewError(domain.CodeInvalidArgument, "invalid restore repository request")
	}
	requestHash := hashJSON(struct{ RepositoryID, ActorID string }{cmd.RepositoryID, cmd.ActorID})
	repository, replay, err := s.beginRepositoryLifecycle(ctx, cmd.TenantID, cmd.RepositoryID, cmd.IdempotencyKey, restoreRepositoryCommand, requestHash, domain.RepositorySuspended)
	if err != nil || replay.ID != "" {
		return replay, err
	}
	remote, err := s.Provider.UnarchiveRepository(ctx, repository.ProviderProjectID)
	if err != nil {
		return domain.Repository{}, domain.Wrap(domain.CodeExternal, "git repository restore failed", err)
	}
	if remote.ID != repository.ProviderProjectID || remote.Archived {
		return domain.Repository{}, domain.NewError(domain.CodeConflict, "provider did not confirm repository restore")
	}
	err = s.Store.Transact(ctx, func(tx Tx) error {
		current, ok := tx.GetRepository(repository.ID)
		if !ok || current.TenantID != cmd.TenantID || current.ProviderProjectID != repository.ProviderProjectID {
			return domain.NewError(domain.CodeConflict, "repository provider identity changed")
		}
		expected := current.Version
		if err := current.Resume(s.Clock.Now()); err != nil {
			return err
		}
		if err := tx.UpdateRepository(current, expected); err != nil {
			return err
		}
		payload, marshalErr := json.Marshal(map[string]any{"repository_id": current.ID, "provider_project_id": current.ProviderProjectID})
		if marshalErr != nil {
			return marshalErr
		}
		if err := tx.AppendOutbox(OutboxRecord{ID: s.IDs.NewID("evt"), Topic: "source.repository_restored.v2", AggregateID: current.ID, Payload: payload, CreatedAt: s.Clock.Now()}); err != nil {
			return err
		}
		if err := tx.AppendAudit(AuditRecord{ID: s.IDs.NewID("aud"), TenantID: cmd.TenantID, ActorID: cmd.ActorID, Action: "source.repository.restore", ResourceType: "repository", ResourceID: current.ID, Data: payload, CreatedAt: s.Clock.Now()}); err != nil {
			return err
		}
		repository = current
		return completeRepositoryLifecycleIdempotency(tx, cmd.TenantID, cmd.IdempotencyKey, restoreRepositoryCommand, requestHash, current, s.Clock.Now())
	})
	return repository, err
}

type PurgeRepositoryCommand struct {
	TenantID        string
	ActorID         string
	RepositoryID    string
	ApprovalGrantID string
	IdempotencyKey  string
}

func (s *Service) PurgeRepository(ctx context.Context, cmd PurgeRepositoryCommand) (domain.Repository, error) {
	if s.Store == nil || s.Provider == nil || s.Clock == nil || s.IDs == nil {
		return domain.Repository{}, domain.NewError(domain.CodeUnavailable, "source service is not configured")
	}
	if !safeMRMetadata(cmd.TenantID) || !safeMRMetadata(cmd.ActorID) || !safeMRMetadata(cmd.RepositoryID) || !safeMRMetadata(cmd.ApprovalGrantID) || !safeMRMetadata(cmd.IdempotencyKey) {
		return domain.Repository{}, domain.NewError(domain.CodeInvalidArgument, "invalid purge repository request")
	}
	requestHash := hashJSON(struct{ RepositoryID, ActorID, ApprovalGrantID string }{cmd.RepositoryID, cmd.ActorID, cmd.ApprovalGrantID})
	repository, replay, err := s.beginRepositoryLifecycle(ctx, cmd.TenantID, cmd.RepositoryID, cmd.IdempotencyKey, purgeRepositoryCommand, requestHash, domain.RepositorySuspended)
	if err != nil || replay.ID != "" {
		return replay, err
	}
	if s.PurgeAuthorizer == nil {
		return domain.Repository{}, domain.NewError(domain.CodeForbidden, "project purge approval verifier is not configured")
	}
	if err := s.PurgeAuthorizer.VerifyAndConsumeProjectPurge(ctx, ProjectPurgeAuthorization{
		TenantID: cmd.TenantID, ProjectID: repository.ProjectID, RepositoryID: repository.ID,
		ProviderProjectID: repository.ProviderProjectID, ActorID: cmd.ActorID, ApprovalGrantID: cmd.ApprovalGrantID,
		IdempotencyKey: cmd.IdempotencyKey, Now: s.Clock.Now(),
	}); err != nil {
		return domain.Repository{}, domain.Wrap(domain.CodeForbidden, "project purge approval denied", err)
	}
	if err := s.Provider.DeleteRepository(ctx, repository.ProviderProjectID); err != nil {
		return domain.Repository{}, domain.Wrap(domain.CodeExternal, "git repository purge request failed", err)
	}
	err = s.Store.Transact(ctx, func(tx Tx) error {
		current, ok := tx.GetRepository(repository.ID)
		if !ok || current.TenantID != cmd.TenantID || current.ProviderProjectID != repository.ProviderProjectID {
			return domain.NewError(domain.CodeConflict, "repository provider identity changed")
		}
		expected := current.Version
		if err := current.MarkPurgePending(s.Clock.Now()); err != nil {
			return err
		}
		if err := tx.UpdateRepository(current, expected); err != nil {
			return err
		}
		payload, marshalErr := json.Marshal(map[string]any{"repository_id": current.ID, "provider_project_id": current.ProviderProjectID, "approval_grant_id": cmd.ApprovalGrantID})
		if marshalErr != nil {
			return marshalErr
		}
		if err := tx.AppendOutbox(OutboxRecord{ID: s.IDs.NewID("evt"), Topic: "source.repository_purge_requested.v2", AggregateID: current.ID, Payload: payload, CreatedAt: s.Clock.Now()}); err != nil {
			return err
		}
		if err := tx.AppendAudit(AuditRecord{ID: s.IDs.NewID("aud"), TenantID: cmd.TenantID, ActorID: cmd.ActorID, Action: "source.repository.purge.request", ResourceType: "repository", ResourceID: current.ID, Data: payload, CreatedAt: s.Clock.Now()}); err != nil {
			return err
		}
		repository = current
		return completeRepositoryLifecycleIdempotency(tx, cmd.TenantID, cmd.IdempotencyKey, purgeRepositoryCommand, requestHash, current, s.Clock.Now())
	})
	return repository, err
}

func (s *Service) beginRepositoryLifecycle(ctx context.Context, tenantID, repositoryID, idempotencyKey, command, requestHash string, requiredState domain.RepositoryState) (domain.Repository, domain.Repository, error) {
	var repository domain.Repository
	var replay domain.Repository
	err := s.Store.Transact(ctx, func(tx Tx) error {
		value, ok := tx.GetRepository(repositoryID)
		if !ok || value.TenantID != tenantID {
			return domain.NewError(domain.CodeNotFound, "repository not found")
		}
		if record, ok := tx.GetIdempotency(tenantID, idempotencyKey); ok {
			if record.Command != command || record.RequestHash != requestHash {
				return domain.NewError(domain.CodeConflict, "idempotency key was used for another request")
			}
			if record.Completed {
				return json.Unmarshal(record.Result, &replay)
			}
		} else if err := tx.PutIdempotency(IdempotencyRecord{TenantID: tenantID, Key: idempotencyKey, Command: command, RequestHash: requestHash, CreatedAt: s.Clock.Now().UTC()}); err != nil {
			return err
		}
		if value.State != requiredState {
			return domain.NewError(domain.CodeConflict, "repository lifecycle state does not match command")
		}
		repository = value
		return nil
	})
	return repository, replay, err
}

func completeRepositoryLifecycleIdempotency(tx Tx, tenantID, key, command, requestHash string, repository domain.Repository, now time.Time) error {
	raw, err := json.Marshal(repository)
	if err != nil {
		return err
	}
	return tx.PutIdempotency(IdempotencyRecord{TenantID: tenantID, Key: key, Command: command, RequestHash: requestHash, Result: raw, Completed: true, CreatedAt: now.UTC(), CompletedAt: now.UTC()})
}
