package application

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/keir-research/ai-native-paas/internal/source/domain"
)

type BindPreviewEnvironmentCommand struct {
	TenantID      string
	ActorID       string
	RepositoryID  string
	Branch        string
	EnvironmentID string
}

// BindPreviewEnvironment durably records the source/runtime identity mapping.
// It intentionally has no runtime dependency: branch deletion later produces
// an outbox intent instead of performing an imperative cleanup.
func (s *Service) BindPreviewEnvironment(ctx context.Context, cmd BindPreviewEnvironmentCommand) (domain.BranchHead, error) {
	if s.Store == nil || s.Clock == nil || s.IDs == nil {
		return domain.BranchHead{}, domain.NewError(domain.CodeUnavailable, "source service is not configured")
	}
	cmd.TenantID = strings.TrimSpace(cmd.TenantID)
	cmd.ActorID = strings.TrimSpace(cmd.ActorID)
	cmd.RepositoryID = strings.TrimSpace(cmd.RepositoryID)
	cmd.Branch = strings.TrimSpace(cmd.Branch)
	cmd.EnvironmentID = strings.TrimSpace(cmd.EnvironmentID)
	if cmd.TenantID == "" || cmd.ActorID == "" || cmd.RepositoryID == "" || cmd.Branch == "" || cmd.EnvironmentID == "" {
		return domain.BranchHead{}, domain.NewError(domain.CodeInvalidArgument, "missing preview environment binding fields")
	}
	var result domain.BranchHead
	err := s.Store.Transact(ctx, func(tx Tx) error {
		repository, ok := tx.GetRepository(cmd.RepositoryID)
		if !ok || repository.TenantID != cmd.TenantID {
			return domain.NewError(domain.CodeNotFound, "repository not found")
		}
		if cmd.Branch == repository.DefaultBranch {
			return domain.NewError(domain.CodeInvalidArgument, "default branch cannot be a preview environment")
		}
		branch, ok := tx.GetBranch(repository.ID, cmd.Branch)
		expected := int64(0)
		if !ok {
			var err error
			branch, err = domain.NewBranchHead(repository.ID, cmd.Branch)
			if err != nil {
				return err
			}
		} else {
			expected = branch.Version
		}
		changed, err := branch.BindPreviewEnvironment(cmd.EnvironmentID)
		if err != nil {
			return err
		}
		if changed {
			if err := tx.UpsertBranch(branch, expected); err != nil {
				return err
			}
			data, err := json.Marshal(map[string]string{"branch": cmd.Branch, "environment_id": cmd.EnvironmentID})
			if err != nil {
				return err
			}
			if err := tx.AppendAudit(AuditRecord{ID: s.IDs.NewID("aud"), TenantID: cmd.TenantID, ActorID: cmd.ActorID, Action: "source.preview_environment.bind", ResourceType: "repository", ResourceID: repository.ID, Data: data, CreatedAt: s.Clock.Now()}); err != nil {
				return err
			}
		}
		result = branch
		return nil
	})
	return result, err
}
