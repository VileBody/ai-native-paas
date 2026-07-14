package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/keir-research/ai-native-paas/internal/source/domain"
	commercev2 "github.com/keir-research/ai-native-paas/pkg/contracts/commerce/v2"
	infrastructurev1 "github.com/keir-research/ai-native-paas/pkg/contracts/infrastructure/v1"
)

const publishMergeRequestPlanSummaryCommand = "source.publish_merge_request_plan_summary.v2"

type PublishMergeRequestPlanSummaryCommand struct {
	TenantID       string
	ActorID        string
	RepositoryID   string
	ProviderIID    int64
	Plan           infrastructurev1.PlanSummary
	Estimate       commercev2.CostEstimate
	IdempotencyKey string
}

type PublishMergeRequestPlanSummaryResult struct {
	RepositoryID    string `json:"repository_id"`
	ProviderIID     int64  `json:"provider_iid"`
	ProviderNoteID  int64  `json:"provider_note_id"`
	PlanHash        string `json:"plan_hash"`
	EstimateVersion string `json:"estimate_version"`
}

// PublishMergeRequestPlanSummary renders a value-free, bounded note from
// normalized plan and customer-cost contracts. Raw OpenTofu values, resource
// addresses, provider identifiers and provider costs never reach GitLab.
func (s *Service) PublishMergeRequestPlanSummary(ctx context.Context, cmd PublishMergeRequestPlanSummaryCommand) (PublishMergeRequestPlanSummaryResult, error) {
	if s.Store == nil || s.Provider == nil || s.Clock == nil || s.IDs == nil {
		return PublishMergeRequestPlanSummaryResult{}, domain.NewError(domain.CodeUnavailable, "source service is not configured")
	}
	if err := validatePlanSummaryCommand(cmd, s.Clock.Now()); err != nil {
		return PublishMergeRequestPlanSummaryResult{}, err
	}
	requestHash := hashJSON(struct {
		RepositoryID string
		ActorID      string
		ProviderIID  int64
		Plan         infrastructurev1.PlanSummary
		Estimate     commercev2.CostEstimate
	}{cmd.RepositoryID, cmd.ActorID, cmd.ProviderIID, cmd.Plan, cmd.Estimate})

	var repository domain.Repository
	var replay PublishMergeRequestPlanSummaryResult
	err := s.Store.Transact(ctx, func(tx Tx) error {
		value, ok := tx.GetRepository(cmd.RepositoryID)
		if !ok || value.TenantID != cmd.TenantID || value.State != domain.RepositoryReady {
			return domain.NewError(domain.CodeNotFound, "ready repository not found")
		}
		if value.ProjectID != cmd.Plan.ProjectID || value.ProviderProjectID <= 0 {
			return domain.NewError(domain.CodeForbidden, "plan does not belong to repository project")
		}
		mergeRequest, ok := tx.GetMergeRequest(value.ID, cmd.ProviderIID)
		if !ok || mergeRequest.State != domain.MergeRequestOpen {
			return domain.NewError(domain.CodeNotFound, "open merge request not found")
		}
		if record, ok := tx.GetIdempotency(cmd.TenantID, cmd.IdempotencyKey); ok {
			if record.Command != publishMergeRequestPlanSummaryCommand || record.RequestHash != requestHash {
				return domain.NewError(domain.CodeConflict, "idempotency key was used for another request")
			}
			if record.Completed {
				return json.Unmarshal(record.Result, &replay)
			}
		} else if err := tx.PutIdempotency(IdempotencyRecord{
			TenantID: cmd.TenantID, Key: cmd.IdempotencyKey, Command: publishMergeRequestPlanSummaryCommand,
			RequestHash: requestHash, CreatedAt: s.Clock.Now().UTC(),
		}); err != nil {
			return err
		}
		repository = value
		return nil
	})
	if err != nil || replay.ProviderNoteID != 0 {
		return replay, err
	}

	marker := planSummaryMarker(repository.ID, cmd.ProviderIID, cmd.Plan.PlanID, cmd.Plan.PlanHash, cmd.Estimate.Version)
	body := renderPlanSummaryNote(marker, cmd.Plan, cmd.Estimate)
	note, found, findErr := s.Provider.FindMergeRequestNoteByMarker(ctx, repository.ProviderProjectID, cmd.ProviderIID, marker)
	if findErr != nil {
		return PublishMergeRequestPlanSummaryResult{}, domain.Wrap(domain.CodeExternal, "merge request note recovery lookup failed", findErr)
	}
	if found && note.Body != body {
		return PublishMergeRequestPlanSummaryResult{}, domain.NewError(domain.CodeConflict, "provider note marker is bound to different content")
	}
	if !found {
		note, err = s.Provider.CreateMergeRequestNote(ctx, repository.ProviderProjectID, cmd.ProviderIID, body)
		if err != nil {
			note, found, findErr = s.Provider.FindMergeRequestNoteByMarker(ctx, repository.ProviderProjectID, cmd.ProviderIID, marker)
			if findErr != nil || !found {
				return PublishMergeRequestPlanSummaryResult{}, domain.Wrap(domain.CodeExternal, "merge request plan summary publication failed", err)
			}
		}
	}
	if note.ID <= 0 || note.Body != body {
		return PublishMergeRequestPlanSummaryResult{}, domain.NewError(domain.CodeConflict, "provider note does not match sanitized plan summary")
	}
	result := PublishMergeRequestPlanSummaryResult{
		RepositoryID: repository.ID, ProviderIID: cmd.ProviderIID, ProviderNoteID: note.ID,
		PlanHash: cmd.Plan.PlanHash, EstimateVersion: cmd.Estimate.Version,
	}
	err = s.Store.Transact(ctx, func(tx Tx) error {
		current, ok := tx.GetRepository(repository.ID)
		if !ok || current.TenantID != cmd.TenantID || current.ProviderProjectID != repository.ProviderProjectID {
			return domain.NewError(domain.CodeConflict, "repository provider identity changed")
		}
		mergeRequest, ok := tx.GetMergeRequest(repository.ID, cmd.ProviderIID)
		if !ok || mergeRequest.State != domain.MergeRequestOpen {
			return domain.NewError(domain.CodeConflict, "merge request state changed")
		}
		payload, marshalErr := json.Marshal(map[string]any{
			"repository_id": repository.ID, "provider_iid": cmd.ProviderIID, "provider_note_id": note.ID,
			"plan_hash": cmd.Plan.PlanHash, "estimate_version": cmd.Estimate.Version,
		})
		if marshalErr != nil {
			return marshalErr
		}
		if err := tx.AppendOutbox(OutboxRecord{ID: s.IDs.NewID("evt"), Topic: "source.merge_request_plan_summary_published.v2", AggregateID: repository.ID, Payload: payload, CreatedAt: s.Clock.Now()}); err != nil {
			return err
		}
		if err := tx.AppendAudit(AuditRecord{
			ID: s.IDs.NewID("aud"), TenantID: cmd.TenantID, ActorID: cmd.ActorID,
			Action: "source.merge_request.plan_summary.publish", ResourceType: "merge_request", ResourceID: repository.ID,
			Data: payload, CreatedAt: s.Clock.Now(),
		}); err != nil {
			return err
		}
		raw, marshalErr := json.Marshal(result)
		if marshalErr != nil {
			return marshalErr
		}
		return tx.PutIdempotency(IdempotencyRecord{
			TenantID: cmd.TenantID, Key: cmd.IdempotencyKey, Command: publishMergeRequestPlanSummaryCommand,
			RequestHash: requestHash, Result: raw, Completed: true, CreatedAt: s.Clock.Now().UTC(), CompletedAt: s.Clock.Now().UTC(),
		})
	})
	return result, err
}

func validatePlanSummaryCommand(cmd PublishMergeRequestPlanSummaryCommand, now time.Time) error {
	if !safeMRMetadata(cmd.TenantID) || !safeMRMetadata(cmd.ActorID) || !safeMRMetadata(cmd.RepositoryID) || !safeMRMetadata(cmd.IdempotencyKey) || cmd.ProviderIID <= 0 || len(cmd.Plan.Changes) == 0 || len(cmd.Plan.Changes) > 1000 {
		return domain.NewError(domain.CodeInvalidArgument, "invalid merge request plan summary request")
	}
	if err := cmd.Plan.PlanRef.Validate(); err != nil || cmd.Plan.CreatedAt.After(now.Add(5*time.Minute)) || cmd.Estimate.Validate(cmd.Plan.CreatedAt) != nil || cmd.Plan.PlanHash != cmd.Estimate.PlanHash || cmd.Plan.EstimateVersion != cmd.Estimate.Version {
		return domain.NewError(domain.CodeInvalidArgument, "plan and estimate contracts do not match")
	}
	for _, change := range cmd.Plan.Changes {
		switch change.Action {
		case infrastructurev1.ActionCreate, infrastructurev1.ActionUpdate, infrastructurev1.ActionReplace, infrastructurev1.ActionDelete, infrastructurev1.ActionNoOp:
		default:
			return domain.NewError(domain.CodeInvalidArgument, "plan contains unknown action")
		}
	}
	return nil
}

func planSummaryMarker(repositoryID string, providerIID int64, planID, planHash, estimateVersion string) string {
	raw, _ := json.Marshal([]any{repositoryID, providerIID, planID, planHash, estimateVersion})
	digest := sha256.Sum256(raw)
	return "<!-- ai-native-paas-plan-summary:v1 sha256:" + hex.EncodeToString(digest[:]) + " -->"
}

func renderPlanSummaryNote(marker string, plan infrastructurev1.PlanSummary, estimate commercev2.CostEstimate) string {
	counts := map[infrastructurev1.ChangeAction]int{}
	for _, change := range plan.Changes {
		counts[change.Action]++
	}
	approval := "not required"
	if plan.RequiresApproval || estimate.ApprovalRequired {
		approval = "required"
	}
	var builder strings.Builder
	builder.WriteString(marker)
	builder.WriteString("\n### Platform plan summary\n\n")
	builder.WriteString("| Action | Resources |\n|---|---:|\n")
	for _, action := range []infrastructurev1.ChangeAction{infrastructurev1.ActionCreate, infrastructurev1.ActionUpdate, infrastructurev1.ActionReplace, infrastructurev1.ActionDelete, infrastructurev1.ActionNoOp} {
		builder.WriteString("| ")
		builder.WriteString(string(action))
		builder.WriteString(" | ")
		builder.WriteString(strconv.Itoa(counts[action]))
		builder.WriteString(" |\n")
	}
	builder.WriteString("\nCustomer cost range: `")
	builder.WriteString(estimate.Minimum.Currency)
	builder.WriteByte(' ')
	builder.WriteString(strconv.FormatInt(estimate.Minimum.MinorUnit, 10))
	builder.WriteString("–")
	builder.WriteString(strconv.FormatInt(estimate.Maximum.MinorUnit, 10))
	builder.WriteString(" minor units`\n\nApproval: `")
	builder.WriteString(approval)
	builder.WriteString("`\n\nPlan hash: `")
	builder.WriteString(plan.PlanHash)
	builder.WriteString("`\n\nEstimate version: `")
	builder.WriteString(estimate.Version)
	builder.WriteString("`\n\nEstimate expires at: `")
	builder.WriteString(estimate.ExpiresAt.UTC().Format(time.RFC3339))
	builder.WriteString("`\n\nResource addresses, provider identifiers, raw values, provider costs, credentials, and command output are intentionally omitted.")
	return builder.String()
}
