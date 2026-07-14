package application

import (
	"encoding/json"
	"time"

	"github.com/keir-research/ai-native-paas/internal/source/domain"
	sourcev2 "github.com/keir-research/ai-native-paas/pkg/contracts/source/v2"
)

func appendRevisionObserved(tx Tx, ids IDGenerator, repository domain.Repository, branch, commitSHA, reason, providerEventID string, now time.Time) error {
	event := sourcev2.RevisionObservedEvent{
		TenantID:        repository.TenantID,
		ProjectID:       repository.ProjectID,
		RepositoryID:    repository.ID,
		Branch:          branch,
		CommitSHA:       commitSHA,
		Reason:          reason,
		ProviderEventID: providerEventID,
		ObservedAt:      now.UTC(),
	}
	if err := event.Validate(); err != nil {
		return domain.Wrap(domain.CodeInvalidArgument, "invalid revision observed event", err)
	}
	payload, err := json.Marshal(event)
	if err != nil {
		return err
	}
	return tx.AppendOutbox(OutboxRecord{ID: ids.NewID("evt"), Topic: sourcev2.EventRevisionObserved, AggregateID: repository.ID, Payload: payload, CreatedAt: now})
}

func appendEnvironmentCleanupRequested(tx Tx, ids IDGenerator, repository domain.Repository, branch, environmentID, providerEventID string, now time.Time) error {
	event := sourcev2.EnvironmentCleanupRequestedEvent{
		TenantID:        repository.TenantID,
		ProjectID:       repository.ProjectID,
		RepositoryID:    repository.ID,
		Branch:          branch,
		EnvironmentID:   environmentID,
		Reason:          "branch_deleted",
		ProviderEventID: providerEventID,
		RequestedAt:     now.UTC(),
	}
	if err := event.Validate(); err != nil {
		return domain.Wrap(domain.CodeInvalidArgument, "invalid environment cleanup request", err)
	}
	payload, err := json.Marshal(event)
	if err != nil {
		return err
	}
	return tx.AppendOutbox(OutboxRecord{ID: ids.NewID("evt"), Topic: sourcev2.EventEnvironmentCleanupRequested, AggregateID: repository.ID, Payload: payload, CreatedAt: now})
}
