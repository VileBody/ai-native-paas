package application

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/keir-research/ai-native-paas/internal/source/domain"
)

type Reconciler struct {
	Store    Store
	Provider GitProvider
	Clock    Clock
	IDs      IDGenerator
}
type ReconcileResult struct {
	Repositories      int
	MetadataChanges   int
	BranchChanges     int
	Quarantines       int
	QuarantineChanges int
	Errors            int
}

type providerPathCandidate struct {
	repository domain.Repository
}

func (r *Reconciler) ReconcileAll(ctx context.Context) (ReconcileResult, error) {
	var repos []domain.Repository
	candidates := map[int64]map[string][]providerPathCandidate{}
	if err := r.Store.Transact(ctx, func(tx Tx) error {
		repos = tx.ListRepositories()
		for _, repository := range repos {
			project, ok := tx.GetProject(repository.ProjectID)
			if !ok {
				return domain.NewError(domain.CodeNotFound, "repository project not found")
			}
			if candidates[repository.ProviderNamespaceID] == nil {
				candidates[repository.ProviderNamespaceID] = map[string][]providerPathCandidate{}
			}
			candidates[repository.ProviderNamespaceID][project.Slug] = append(candidates[repository.ProviderNamespaceID][project.Slug], providerPathCandidate{repository: repository})
		}
		return nil
	}); err != nil {
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
	known := make(map[int64]struct{}, len(repos))
	for _, repository := range repos {
		if repository.ProviderProjectID > 0 {
			known[repository.ProviderProjectID] = struct{}{}
		}
	}
	for namespaceID, byPath := range candidates {
		remoteRepositories, err := r.Provider.ListRepositoriesByNamespace(ctx, namespaceID)
		if err != nil {
			result.Errors++
			continue
		}
		for _, remote := range remoteRepositories {
			if _, recognized := known[remote.ID]; recognized {
				continue
			}
			matching := byPath[remote.Path]
			if len(matching) == 0 {
				continue
			}
			if err := r.quarantineUnknownProviderProject(ctx, namespaceID, remote, matching, &result); err != nil {
				result.Errors++
			}
		}
	}
	return result, nil
}

func (r *Reconciler) quarantineUnknownProviderProject(ctx context.Context, namespaceID int64, remote ProviderRepository, matching []providerPathCandidate, result *ReconcileResult) error {
	candidateRepositoryID := "ambiguous"
	externalIdentityMatched := false
	if len(matching) == 1 {
		candidateRepositoryID = matching[0].repository.ID
		externalIdentityMatched = remote.ExternalID == matching[0].repository.CorrelationID
	}
	managedLabelPresent := providerTopicPresent(remote.Topics, "ai-native-paas")
	reason := domain.QuarantineUnboundManagedProject
	if !externalIdentityMatched {
		reason = domain.QuarantineExternalIdentityMismatch
	} else if !managedLabelPresent {
		reason = domain.QuarantineManagedLabelMissing
	}
	providerPath := remote.PathWithNamespace
	if strings.TrimSpace(providerPath) == "" {
		providerPath = remote.Path
	}
	newOrChanged := false
	err := r.Store.Transact(ctx, func(tx Tx) error {
		quarantine, exists := tx.GetProviderQuarantine("gitlab", remote.ID)
		expected := int64(0)
		if !exists {
			var err error
			quarantine, err = domain.NewProviderQuarantine("gitlab", namespaceID, remote.ID, providerPath, remote.WebURL, candidateRepositoryID, reason, externalIdentityMatched, managedLabelPresent, r.Clock.Now())
			if err != nil {
				return err
			}
			newOrChanged = true
		} else {
			expected = quarantine.Version
			changed, err := quarantine.Observe(providerPath, remote.WebURL, candidateRepositoryID, reason, externalIdentityMatched, managedLabelPresent, r.Clock.Now())
			if err != nil {
				return err
			}
			newOrChanged = changed
		}
		if err := tx.UpsertProviderQuarantine(quarantine, expected); err != nil {
			return err
		}
		if !newOrChanged {
			return nil
		}
		payload, err := json.Marshal(map[string]any{
			"provider": "gitlab", "provider_namespace_id": namespaceID, "provider_project_id": remote.ID,
			"provider_path": providerPath, "candidate_repository_id": candidateRepositoryID, "reason": reason,
			"external_identity_matched": externalIdentityMatched, "managed_label_present": managedLabelPresent,
		})
		if err != nil {
			return err
		}
		resourceID := "gitlab/" + strconv.FormatInt(remote.ID, 10)
		if err = tx.AppendOutbox(OutboxRecord{ID: r.IDs.NewID("evt"), Topic: "source.provider_project_quarantined.v2", AggregateID: resourceID, Payload: payload, CreatedAt: r.Clock.Now()}); err != nil {
			return err
		}
		return tx.AppendAudit(AuditRecord{ID: r.IDs.NewID("aud"), TenantID: "platform", ActorID: "source-reconciler", Action: "source.provider_project.quarantine", ResourceType: "provider_project", ResourceID: resourceID, Data: payload, CreatedAt: r.Clock.Now()})
	})
	if err != nil {
		return err
	}
	result.Quarantines++
	if newOrChanged {
		result.QuarantineChanges++
	}
	return nil
}

func providerTopicPresent(topics []string, expected string) bool {
	for _, topic := range topics {
		if strings.EqualFold(strings.TrimSpace(topic), expected) {
			return true
		}
	}
	return false
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
