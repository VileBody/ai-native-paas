package application

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/keir-research/ai-native-paas/internal/source/domain"
	sourcev2 "github.com/keir-research/ai-native-paas/pkg/contracts/source/v2"
)

const createMergeRequestCommand = "source.create_merge_request.v2"

type CreateMergeRequestCommand struct {
	TenantID, ActorID, RepositoryID string
	SourceBranch, TargetBranch      string
	ExpectedHeadSHA, SourcePlanHash string
	TaskID, CorrelationID, Title    string
	IdempotencyKey                  string
}

type CreateMergeRequestResult struct {
	RepositoryID   string                   `json:"repository_id"`
	ProviderIID    int64                    `json:"provider_iid"`
	SourceBranch   string                   `json:"source_branch"`
	TargetBranch   string                   `json:"target_branch"`
	HeadSHA        string                   `json:"head_sha"`
	State          domain.MergeRequestState `json:"state"`
	WebURL         string                   `json:"web_url"`
	SourcePlanHash string                   `json:"source_plan_hash"`
}

// CreateMergeRequest creates a provider MR only for an exact, already-pushed
// agent commit. Provider identity and the protected target branch are loaded
// from the tenant-scoped repository rather than accepted from the caller.
func (s *Service) CreateMergeRequest(ctx context.Context, cmd CreateMergeRequestCommand) (CreateMergeRequestResult, error) {
	if s.Store == nil || s.Provider == nil || s.Clock == nil || s.IDs == nil {
		return CreateMergeRequestResult{}, domain.NewError(domain.CodeUnavailable, "source service is not configured")
	}
	if !validMergeRequestCommand(cmd) {
		return CreateMergeRequestResult{}, domain.NewError(domain.CodeInvalidArgument, "invalid merge request request")
	}
	requestHash := hashJSON(struct {
		RepositoryID, SourceBranch, TargetBranch, ExpectedHeadSHA, SourcePlanHash, TaskID, CorrelationID, Title string
	}{cmd.RepositoryID, cmd.SourceBranch, cmd.TargetBranch, cmd.ExpectedHeadSHA, cmd.SourcePlanHash, cmd.TaskID, cmd.CorrelationID, cmd.Title})

	var repository domain.Repository
	var replay CreateMergeRequestResult
	err := s.Store.Transact(ctx, func(tx Tx) error {
		value, ok := tx.GetRepository(cmd.RepositoryID)
		if !ok || value.TenantID != cmd.TenantID || value.State != domain.RepositoryReady {
			return domain.NewError(domain.CodeNotFound, "ready repository not found")
		}
		if value.ProviderProjectID <= 0 || cmd.TargetBranch != value.DefaultBranch || cmd.SourceBranch == value.DefaultBranch {
			return domain.NewError(domain.CodeInvalidArgument, "merge request branch policy denied")
		}
		if record, ok := tx.GetIdempotency(cmd.TenantID, cmd.IdempotencyKey); ok {
			if record.Command != createMergeRequestCommand || record.RequestHash != requestHash {
				return domain.NewError(domain.CodeConflict, "idempotency key was used for another request")
			}
			if record.Completed {
				return json.Unmarshal(record.Result, &replay)
			}
		} else if err := tx.PutIdempotency(IdempotencyRecord{
			TenantID: cmd.TenantID, Key: cmd.IdempotencyKey, Command: createMergeRequestCommand,
			RequestHash: requestHash, CreatedAt: s.Clock.Now().UTC(),
		}); err != nil {
			return err
		}
		repository = value
		return nil
	})
	if err != nil || replay.ProviderIID != 0 {
		return replay, err
	}

	head, err := s.Provider.GetBranchHead(ctx, repository.ProviderProjectID, cmd.SourceBranch)
	if err != nil {
		return CreateMergeRequestResult{}, domain.Wrap(domain.CodeExternal, "source branch lookup failed", err)
	}
	if head != cmd.ExpectedHeadSHA {
		return CreateMergeRequestResult{}, domain.NewError(domain.CodeConflict, "source branch no longer matches the attested commit")
	}

	description := governedMergeRequestDescription(cmd)
	remote, found, findErr := s.Provider.FindOpenMergeRequest(ctx, repository.ProviderProjectID, cmd.SourceBranch, cmd.TargetBranch)
	if findErr != nil {
		return CreateMergeRequestResult{}, domain.Wrap(domain.CodeExternal, "merge request recovery lookup failed", findErr)
	}
	if !found {
		remote, err = s.Provider.CreateMergeRequest(ctx, CreateMergeRequestRequest{
			ProjectID: repository.ProviderProjectID, SourceBranch: cmd.SourceBranch, TargetBranch: cmd.TargetBranch,
			Title: cmd.Title, Description: description,
		})
		if err != nil {
			// GitLab may have accepted the request before its response was lost, or
			// another identical invocation may have won the race. Reconcile by the
			// exact source/target pair before surfacing an external failure.
			remote, found, findErr = s.Provider.FindOpenMergeRequest(ctx, repository.ProviderProjectID, cmd.SourceBranch, cmd.TargetBranch)
			if findErr != nil || !found {
				return CreateMergeRequestResult{}, domain.Wrap(domain.CodeExternal, "merge request creation failed", err)
			}
		}
	}
	if remote.IID <= 0 || remote.SourceBranch != cmd.SourceBranch || remote.TargetBranch != cmd.TargetBranch || remote.HeadSHA != cmd.ExpectedHeadSHA || remote.Description != description || strings.TrimSpace(remote.WebURL) == "" {
		return CreateMergeRequestResult{}, domain.NewError(domain.CodeConflict, "provider merge request does not match the attested change")
	}

	state, err := mergeRequestState(remote.State)
	if err != nil {
		return CreateMergeRequestResult{}, err
	}
	result := CreateMergeRequestResult{
		RepositoryID: repository.ID, ProviderIID: remote.IID, SourceBranch: remote.SourceBranch,
		TargetBranch: remote.TargetBranch, HeadSHA: remote.HeadSHA, State: state,
		WebURL: remote.WebURL, SourcePlanHash: cmd.SourcePlanHash,
	}
	err = s.Store.Transact(ctx, func(tx Tx) error {
		current, ok := tx.GetRepository(repository.ID)
		if !ok || current.TenantID != cmd.TenantID || current.ProviderProjectID != repository.ProviderProjectID {
			return domain.NewError(domain.CodeConflict, "repository provider identity changed")
		}
		if existing, ok := tx.GetMergeRequest(repository.ID, remote.IID); ok {
			if existing.SourceBranch != result.SourceBranch || existing.TargetBranch != result.TargetBranch || existing.HeadSHA != result.HeadSHA {
				return domain.NewError(domain.CodeConflict, "merge request identity conflict")
			}
		} else {
			value := domain.MergeRequest{RepositoryID: repository.ID, ProviderIID: remote.IID, SourceBranch: remote.SourceBranch, TargetBranch: remote.TargetBranch}
			if err := value.Apply("open", remote.State, remote.HeadSHA, s.Clock.Now()); err != nil {
				return err
			}
			if err := tx.UpsertMergeRequest(value, 0); err != nil {
				return err
			}
			payload, _ := json.Marshal(map[string]any{
				"repository_id": repository.ID, "provider_iid": remote.IID, "head_sha": remote.HeadSHA,
				"source_plan_hash": cmd.SourcePlanHash,
			})
			if err := tx.AppendOutbox(OutboxRecord{ID: s.IDs.NewID("evt"), Topic: "source.merge_request_created.v2", AggregateID: repository.ID, Payload: payload, CreatedAt: s.Clock.Now()}); err != nil {
				return err
			}
			if err := tx.AppendAudit(AuditRecord{
				ID: s.IDs.NewID("aud"), TenantID: cmd.TenantID, ActorID: cmd.ActorID,
				Action: "source.merge_request.create", ResourceType: "merge_request", ResourceID: repository.ID,
				Data: payload, CreatedAt: s.Clock.Now(),
			}); err != nil {
				return err
			}
		}
		raw, _ := json.Marshal(result)
		return tx.PutIdempotency(IdempotencyRecord{
			TenantID: cmd.TenantID, Key: cmd.IdempotencyKey, Command: createMergeRequestCommand,
			RequestHash: requestHash, Result: raw, Completed: true, CreatedAt: s.Clock.Now().UTC(), CompletedAt: s.Clock.Now().UTC(),
		})
	})
	return result, err
}

func validMergeRequestCommand(cmd CreateMergeRequestCommand) bool {
	return safeMRMetadata(cmd.TenantID) && safeMRMetadata(cmd.ActorID) && safeMRMetadata(cmd.RepositoryID) &&
		sourcev2.ValidBranch(cmd.SourceBranch) && sourcev2.ValidBranch(cmd.TargetBranch) &&
		validSourceSHA(cmd.ExpectedHeadSHA) && validSourcePlanHash(cmd.SourcePlanHash) &&
		safeMRMetadata(cmd.TaskID) && safeMRMetadata(cmd.CorrelationID) && safeMRTitle(cmd.Title) && safeMRMetadata(cmd.IdempotencyKey)
}

func safeMRMetadata(value string) bool {
	return value != "" && value == strings.TrimSpace(value) && len(value) <= 128 && !strings.ContainsAny(value, "\r\n\x00")
}

func safeMRTitle(value string) bool {
	return value != "" && value == strings.TrimSpace(value) && len(value) <= 200 && !strings.ContainsAny(value, "\r\n\x00")
}

func validSourceSHA(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' && character < 'a' || character > 'f' {
			return false
		}
	}
	return true
}

func validSourcePlanHash(value string) bool {
	return strings.HasPrefix(value, "sha256:") && len(value) == len("sha256:")+64 && validSourceSHA(strings.TrimPrefix(value, "sha256:"))
}

func governedMergeRequestDescription(cmd CreateMergeRequestCommand) string {
	return "Platform-governed agent change.\n\n" +
		"Source-Plan-Hash: `" + cmd.SourcePlanHash + "`\n" +
		"Expected-Head-SHA: `" + cmd.ExpectedHeadSHA + "`\n" +
		"Task-ID: `" + cmd.TaskID + "`\n" +
		"Correlation-ID: `" + cmd.CorrelationID + "`\n" +
		"Actor-ID: `" + cmd.ActorID + "`\n\n" +
		"File contents, credentials, command output, and provider values are intentionally omitted."
}

func mergeRequestState(value string) (domain.MergeRequestState, error) {
	probe := domain.MergeRequest{}
	if err := probe.Apply("open", value, "", time.Time{}); err != nil {
		return "", err
	}
	return probe.State, nil
}
