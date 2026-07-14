package application

import (
	"context"
	"encoding/json"

	"github.com/keir-research/ai-native-paas/internal/source/domain"
)

type RecordBootstrapRevisionCommand struct {
	TenantID     string
	ActorID      string
	RepositoryID string
	Revision     string
}

// RecordBootstrapRevision atomically binds the immutable GitLab bootstrap
// commit to the platform repository and its authoritative default-branch head.
func (s *Service) RecordBootstrapRevision(ctx context.Context, cmd RecordBootstrapRevisionCommand) (domain.Repository, error) {
	if s.Store == nil || s.Clock == nil || s.IDs == nil {
		return domain.Repository{}, domain.NewError(domain.CodeUnavailable, "source service is not configured")
	}
	if !safeMRMetadata(cmd.TenantID) || !safeMRMetadata(cmd.ActorID) || !safeMRMetadata(cmd.RepositoryID) {
		return domain.Repository{}, domain.NewError(domain.CodeInvalidArgument, "invalid bootstrap revision request")
	}
	var repository domain.Repository
	err := s.Store.Transact(ctx, func(tx Tx) error {
		current, ok := tx.GetRepository(cmd.RepositoryID)
		if !ok || current.TenantID != cmd.TenantID {
			return domain.NewError(domain.CodeNotFound, "repository not found")
		}
		expected := current.Version
		changed, err := current.RecordBootstrapRevision(cmd.Revision, s.Clock.Now())
		if err != nil {
			return err
		}
		if !changed {
			repository = current
			return nil
		}
		if err = tx.UpdateRepository(current, expected); err != nil {
			return err
		}
		branch, exists := tx.GetBranch(current.ID, current.DefaultBranch)
		branchExpected := int64(0)
		if !exists {
			branch, err = domain.NewBranchHead(current.ID, current.DefaultBranch)
			if err != nil {
				return err
			}
		} else {
			branchExpected = branch.Version
		}
		if _, err = branch.SetAuthoritative(current.BootstrapRevision, s.Clock.Now()); err != nil {
			return err
		}
		if err = tx.UpsertBranch(branch, branchExpected); err != nil {
			return err
		}
		if err = appendRevisionObserved(tx, s.IDs, current, current.DefaultBranch, current.BootstrapRevision, "bootstrap", "", s.Clock.Now()); err != nil {
			return err
		}
		payload, err := json.Marshal(map[string]any{
			"repository_id": current.ID, "provider_project_id": current.ProviderProjectID,
			"bootstrap_revision": current.BootstrapRevision,
		})
		if err != nil {
			return err
		}
		if err = tx.AppendOutbox(OutboxRecord{ID: s.IDs.NewID("evt"), Topic: "source.repository_bootstrapped.v2", AggregateID: current.ID, Payload: payload, CreatedAt: s.Clock.Now()}); err != nil {
			return err
		}
		if err = tx.AppendAudit(AuditRecord{ID: s.IDs.NewID("aud"), TenantID: cmd.TenantID, ActorID: cmd.ActorID, Action: "source.repository.bootstrap.record", ResourceType: "repository", ResourceID: current.ID, Data: payload, CreatedAt: s.Clock.Now()}); err != nil {
			return err
		}
		repository = current
		return nil
	})
	return repository, err
}
