package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"

	"github.com/keir-research/ai-native-paas/internal/source/domain"
)

type WebhookService struct {
	Store      Store
	Provider   GitProvider
	Verifier   WebhookVerifier
	Normalizer WebhookNormalizer
	Clock      Clock
	IDs        IDGenerator
}
type WebhookResult struct {
	EventID      string
	Duplicate    bool
	Stale        bool
	RepositoryID string
	Kind         string
}

func (s *WebhookService) Handle(ctx context.Context, tenantID string, headers map[string][]string, raw []byte) (WebhookResult, error) {
	if s.Store == nil || s.Provider == nil || s.Verifier == nil || s.Normalizer == nil || s.Clock == nil || s.IDs == nil {
		return WebhookResult{}, domain.NewError(domain.CodeUnavailable, "webhook service is not configured")
	}
	now := s.Clock.Now()
	eventID, err := s.Verifier.Verify(headers, raw, now)
	if err != nil {
		return WebhookResult{}, domain.Wrap(domain.CodeForbidden, "webhook verification failed", err)
	}
	normalized, err := s.Normalizer.Normalize(eventID, raw, now)
	if err != nil {
		return WebhookResult{}, domain.Wrap(domain.CodeInvalidArgument, "webhook normalization failed", err)
	}
	bodySum := sha256.Sum256(raw)
	bodyHash := hex.EncodeToString(bodySum[:])
	if normalized.Push != nil {
		return s.handlePush(ctx, tenantID, *normalized.Push, bodyHash)
	}
	if normalized.MergeRequest != nil {
		return s.handleMR(ctx, tenantID, *normalized.MergeRequest, bodyHash)
	}
	return WebhookResult{}, domain.NewError(domain.CodeInvalidArgument, "empty normalized webhook")
}
func (s *WebhookService) handlePush(ctx context.Context, tenantID string, event PushEvent, bodyHash string) (WebhookResult, error) {
	var repo domain.Repository
	if err := s.Store.Transact(ctx, func(tx Tx) error {
		r, ok := tx.FindRepositoryByProviderID(event.Provider, event.ProviderProjectID)
		if !ok || r.TenantID != tenantID {
			return domain.NewError(domain.CodeNotFound, "repository for webhook not found")
		}
		repo = r
		return nil
	}); err != nil {
		return WebhookResult{}, err
	}
	deletion := isZeroCommitSHA(event.AfterSHA)
	authoritative := ""
	if !deletion {
		var err error
		authoritative, err = s.Provider.GetBranchHead(ctx, event.ProviderProjectID, event.Branch)
		if err != nil {
			return WebhookResult{}, domain.Wrap(domain.CodeExternal, "cannot resolve authoritative branch head", err)
		}
	}
	result := WebhookResult{EventID: event.EventID, RepositoryID: repo.ID, Kind: "push"}
	err := s.Store.Transact(ctx, func(tx Tx) error {
		fresh, err := tx.ReceiveWebhook(WebhookReceipt{Provider: event.Provider, EventID: event.EventID, BodyHash: bodyHash, ReceivedAt: s.Clock.Now()})
		if err != nil {
			return err
		}
		if !fresh {
			result.Duplicate = true
			return nil
		}
		branch, ok := tx.GetBranch(repo.ID, event.Branch)
		expected := int64(0)
		if !ok {
			branch, _ = domain.NewBranchHead(repo.ID, event.Branch)
		} else {
			expected = branch.Version
		}
		now := s.Clock.Now()
		var observed domain.AuthoritativePushResult
		var observeErr error
		if deletion {
			observed, observeErr = branch.ObserveDeletion(event.EventID, event.OccurredAt, now)
		} else {
			observed, observeErr = branch.ObserveAuthoritativePush(authoritative, event.EventID, event.OccurredAt, now)
		}
		if observeErr != nil {
			return observeErr
		}
		if observed.Stale {
			result.Stale = true
			data, marshalErr := json.Marshal(map[string]string{"branch": event.Branch, "provider_event_id": event.EventID})
			if marshalErr != nil {
				return marshalErr
			}
			return tx.AppendAudit(AuditRecord{ID: s.IDs.NewID("aud"), TenantID: tenantID, ActorID: "gitlab-webhook", Action: "source.push.stale", ResourceType: "repository", ResourceID: repo.ID, Data: data, CreatedAt: now})
		}
		if observed.StateChanged {
			if err = tx.UpsertBranch(branch, expected); err != nil {
				return err
			}
		}
		if deletion {
			if observed.StateChanged && branch.EnvironmentID != "" {
				if err := appendEnvironmentCleanupRequested(tx, s.IDs, repo, event.Branch, branch.EnvironmentID, event.EventID, now); err != nil {
					return err
				}
			}
		} else {
			if observed.HeadChanged {
				if err := appendRevisionObserved(tx, s.IDs, repo, event.Branch, authoritative, "webhook", event.EventID, now); err != nil {
					return err
				}
			}
			payload, marshalErr := json.Marshal(map[string]any{"repository_id": repo.ID, "branch": event.Branch, "commit_sha": authoritative, "provider_event_id": event.EventID})
			if marshalErr != nil {
				return marshalErr
			}
			if err := tx.AppendOutbox(OutboxRecord{ID: s.IDs.NewID("evt"), Topic: "source.push.v1", AggregateID: repo.ID, Payload: payload, CreatedAt: now}); err != nil {
				return err
			}
		}
		action := "source.push.observe"
		if deletion {
			action = "source.push.delete.observe"
		}
		return tx.AppendAudit(AuditRecord{ID: s.IDs.NewID("aud"), TenantID: tenantID, ActorID: "gitlab-webhook", Action: action, ResourceType: "repository", ResourceID: repo.ID, Data: []byte(`{}`), CreatedAt: now})
	})
	return result, err
}

func isZeroCommitSHA(value string) bool {
	value = strings.TrimSpace(value)
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	return strings.Trim(value, "0") == ""
}
func (s *WebhookService) handleMR(ctx context.Context, tenantID string, event MergeRequestEvent, bodyHash string) (WebhookResult, error) {
	result := WebhookResult{EventID: event.EventID, Kind: "merge_request"}
	err := s.Store.Transact(ctx, func(tx Tx) error {
		repo, ok := tx.FindRepositoryByProviderID(event.Provider, event.ProviderProjectID)
		if !ok || repo.TenantID != tenantID {
			return domain.NewError(domain.CodeNotFound, "repository for webhook not found")
		}
		result.RepositoryID = repo.ID
		fresh, err := tx.ReceiveWebhook(WebhookReceipt{Provider: event.Provider, EventID: event.EventID, BodyHash: bodyHash, ReceivedAt: s.Clock.Now()})
		if err != nil {
			return err
		}
		if !fresh {
			result.Duplicate = true
			return nil
		}
		mr, ok := tx.GetMergeRequest(repo.ID, event.IID)
		expected := int64(0)
		if !ok {
			mr = domain.MergeRequest{RepositoryID: repo.ID, ProviderIID: event.IID, SourceBranch: event.SourceBranch, TargetBranch: event.TargetBranch, Version: 1}
			expected = 0
		} else {
			expected = mr.Version
		}
		if err := mr.Apply(event.Action, event.State, event.HeadSHA, s.Clock.Now()); err != nil {
			return err
		}
		if err := tx.UpsertMergeRequest(mr, expected); err != nil {
			return err
		}
		payload, _ := json.Marshal(map[string]any{"repository_id": repo.ID, "iid": event.IID, "state": mr.State, "head_sha": mr.HeadSHA})
		return tx.AppendOutbox(OutboxRecord{ID: s.IDs.NewID("evt"), Topic: "source.merge_request.v1", AggregateID: repo.ID, Payload: payload, CreatedAt: s.Clock.Now()})
	})
	return result, err
}

// ProcessPushWithoutProvider is deliberately exposed only for deterministic domain tests.
// Production webhook handling always resolves the provider's current head first.
func ProcessPushWithoutProvider(branch *domain.BranchHead, e PushEvent, now time.Time) (bool, error) {
	return branch.ApplyPush(e.BeforeSHA, e.AfterSHA, e.EventID, e.OccurredAt, now)
}
