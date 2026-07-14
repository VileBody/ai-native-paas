package application

import (
	"context"
	"encoding/json"

	"github.com/keir-research/ai-native-paas/internal/source/domain"
)

type Reconciler struct {
	Store    Store
	Provider GitProvider
	Clock    Clock
	IDs      IDGenerator
}
type ReconcileResult struct {
	Repositories    int
	MetadataChanges int
	BranchChanges   int
	Errors          int
}

func (r *Reconciler) ReconcileAll(ctx context.Context) (ReconcileResult, error) {
	var repos []domain.Repository
	if err := r.Store.Transact(ctx, func(tx Tx) error { repos = tx.ListRepositories(); return nil }); err != nil {
		return ReconcileResult{}, err
	}
	var result ReconcileResult
	for _, repo := range repos {
		if repo.ProviderProjectID <= 0 || repo.State != domain.RepositoryReady {
			continue
		}
		result.Repositories++
		if err := r.reconcileOne(ctx, repo, &result); err != nil {
			result.Errors++
		}
	}
	return result, nil
}
func (r *Reconciler) reconcileOne(ctx context.Context, repo domain.Repository, result *ReconcileResult) error {
	remote, err := r.Provider.GetRepository(ctx, repo.ProviderProjectID)
	if err != nil {
		return err
	}
	head, err := r.Provider.GetBranchHead(ctx, repo.ProviderProjectID, remote.DefaultBranch)
	if err != nil {
		return err
	}
	return r.Store.Transact(ctx, func(tx Tx) error {
		current, ok := tx.GetRepository(repo.ID)
		if !ok {
			return domain.NewError(domain.CodeNotFound, "repository not found")
		}
		expected := current.Version
		before := current
		if err := current.SyncProviderMetadata(remote.ID, remote.PathWithNamespace, remote.WebURL, remote.DefaultBranch, r.Clock.Now()); err != nil {
			return err
		}
		if current.Version != before.Version {
			if err := tx.UpdateRepository(current, expected); err != nil {
				return err
			}
			result.MetadataChanges++
		}
		branch, ok := tx.GetBranch(repo.ID, remote.DefaultBranch)
		branchExpected := int64(0)
		if !ok {
			branch, _ = domain.NewBranchHead(repo.ID, remote.DefaultBranch)
		} else {
			branchExpected = branch.Version
		}
		changed, err := branch.SetAuthoritative(head, r.Clock.Now())
		if err != nil {
			return err
		}
		if changed {
			if err = tx.UpsertBranch(branch, branchExpected); err != nil {
				return err
			}
			result.BranchChanges++
			if err = appendRevisionObserved(tx, r.IDs, current, remote.DefaultBranch, head, "reconciliation", "", r.Clock.Now()); err != nil {
				return err
			}
			payload, _ := json.Marshal(map[string]any{"repository_id": repo.ID, "branch": remote.DefaultBranch, "commit_sha": head, "reason": "reconciliation"})
			if err = tx.AppendOutbox(OutboxRecord{ID: r.IDs.NewID("evt"), Topic: "source.push.v1", AggregateID: repo.ID, Payload: payload, CreatedAt: r.Clock.Now()}); err != nil {
				return err
			}
		}
		return nil
	})
}
