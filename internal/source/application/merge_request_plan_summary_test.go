package application_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/keir-research/ai-native-paas/internal/source/application"
	"github.com/keir-research/ai-native-paas/internal/source/domain"
	commercev2 "github.com/keir-research/ai-native-paas/pkg/contracts/commerce/v2"
	infrastructurev1 "github.com/keir-research/ai-native-paas/pkg/contracts/infrastructure/v1"
)

func planSummaryFixture(projectID, head, planHash, sentinel string, now time.Time) (infrastructurev1.PlanSummary, commercev2.CostEstimate) {
	plan := infrastructurev1.PlanSummary{
		PlanRef:          infrastructurev1.PlanRef{PlanID: "plan-1", ProjectID: projectID, WorkspaceID: "workspace-1", SourceSHA: head, PlanHash: planHash, StateGeneration: 1, CreatedAt: now},
		Changes:          []infrastructurev1.ResourceChange{{Address: sentinel, Provider: sentinel, ResourceType: "twc_server", Action: infrastructurev1.ActionCreate, ExternalID: sentinel}},
		RequiresApproval: true, EstimateVersion: "estimate-version-1", EstimateFingerprint: "sha256:" + strings.Repeat("d", 64),
	}
	estimate := commercev2.CostEstimate{
		EstimateID: "estimate-1", Version: plan.EstimateVersion, PlanHash: planHash, RateCardID: "beta",
		RateCardVersion: "2026-07-14", PriceSnapshotID: "provider-2026-07-14", MarkupBasisPoints: 2500,
		Lines:   []commercev2.EstimateLine{{Meter: sentinel, Quantity: 1, Unit: "month", ProviderCost: commercev2.Money{Currency: "RUB", MinorUnit: 100}, CustomerCost: commercev2.Money{Currency: "RUB", MinorUnit: 125}, PriceKnown: true}},
		Minimum: commercev2.Money{Currency: "RUB", MinorUnit: 125}, Maximum: commercev2.Money{Currency: "RUB", MinorUnit: 250}, ApprovalRequired: true, ExpiresAt: now.Add(time.Hour),
	}
	return plan, estimate
}

func TestMergeRequestPlanSummary_LostResponseRecoversExactSanitizedNote(t *testing.T) {
	service, store, provider, clock, _ := setupService()
	created := create(t, service, "Plan summary", "create-plan-summary-project")
	repository, err := service.ProvisionRepository(context.Background(), application.ProvisionRepositoryCommand{TenantID: "t1", ActorID: "u1", RepositoryID: created.Repository.ID})
	if err != nil {
		t.Fatal(err)
	}
	branch := "agent/plan-summary"
	head := strings.Repeat("b", 40)
	planHash := "sha256:" + strings.Repeat("c", 64)
	provider.SetHead(repository.ProviderProjectID, branch, head)
	mergeRequest, err := service.CreateMergeRequest(context.Background(), application.CreateMergeRequestCommand{
		TenantID: "t1", ActorID: "agent-1", RepositoryID: repository.ID, SourceBranch: branch, TargetBranch: repository.DefaultBranch,
		ExpectedHeadSHA: head, SourcePlanHash: planHash, TaskID: "task-1", CorrelationID: "correlation-1", Title: "Plan summary", IdempotencyKey: "create-mr-summary",
	})
	if err != nil {
		t.Fatal(err)
	}
	const sentinel = "secret-provider-value"
	plan, estimate := planSummaryFixture(repository.ProjectID, head, planHash, sentinel, clock.Now())
	provider.MergeRequestNoteLostResponseOnce = true
	command := application.PublishMergeRequestPlanSummaryCommand{
		TenantID: "t1", ActorID: "agent-1", RepositoryID: repository.ID, ProviderIID: mergeRequest.ProviderIID,
		Plan: plan, Estimate: estimate, IdempotencyKey: "publish-summary",
	}
	first, err := service.PublishMergeRequestPlanSummary(context.Background(), command)
	if err != nil || first.ProviderNoteID <= 0 || provider.MergeRequestNoteCalls != 1 {
		t.Fatalf("first=%+v calls=%d err=%v", first, provider.MergeRequestNoteCalls, err)
	}
	clock.Advance(2 * time.Hour)
	second, err := service.PublishMergeRequestPlanSummary(context.Background(), command)
	if err != nil || second != first || provider.MergeRequestNoteCalls != 1 {
		t.Fatalf("second=%+v calls=%d err=%v", second, provider.MergeRequestNoteCalls, err)
	}
	for _, note := range provider.MergeRequestNotes {
		if strings.Contains(note.Body, sentinel) || !strings.Contains(note.Body, "| CREATE | 1 |") {
			t.Fatalf("note was not sanitized: %s", note.Body)
		}
	}
	_, _, outbox, audit := store.Snapshot()
	if outbox[len(outbox)-1].Topic != "source.merge_request_plan_summary_published.v2" || audit[len(audit)-1].Action != "source.merge_request.plan_summary.publish" {
		t.Fatalf("outbox=%+v audit=%+v", outbox[len(outbox)-1], audit[len(audit)-1])
	}
	command.ActorID = "other-agent"
	if _, err = service.PublishMergeRequestPlanSummary(context.Background(), command); !domain.HasCode(err, domain.CodeConflict) {
		t.Fatalf("actor-changed idempotency replay err=%v", err)
	}
}
