package pivot_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	infraapp "github.com/keir-research/ai-native-paas/internal/infrastructure/application"
	"github.com/keir-research/ai-native-paas/internal/workspace"
	infrastructurev1 "github.com/keir-research/ai-native-paas/pkg/contracts/infrastructure/v1"
)

func TestInfraPlan_IsPureIdempotentAndStoresCanonicalPlanHash(t *testing.T) {
	service, _, _ := infrastructureFixture()
	first, err := service.Plan(context.Background(), planCommand("same-command", "staging", twoResourcePlan))
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.Plan(context.Background(), planCommand("same-command", "staging", twoResourcePlan))
	if err != nil {
		t.Fatal(err)
	}
	if first.Summary.PlanID != second.Summary.PlanID || first.Summary.PlanHash != second.Summary.PlanHash || first.Reservation.ReservationID != second.Reservation.ReservationID {
		t.Fatalf("idempotent plan changed durable identity: first=%#v second=%#v", first, second)
	}
	changed := planCommand("same-command", "staging", twoResourcePlan)
	changed.ArtifactDigest = "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	if _, err = service.Plan(context.Background(), changed); !errors.Is(err, infraapp.ErrConflict) {
		t.Fatalf("idempotency payload mismatch was accepted: %v", err)
	}
}

func TestInfraApply_RequiresMatchingPlanReservationAndApproval(t *testing.T) {
	service, _, now := infrastructureFixture()
	result, err := service.Plan(context.Background(), planCommand("production-plan", "production", twoResourcePlan))
	if err != nil {
		t.Fatal(err)
	}
	authorization := infrastructurev1.ApplyAuthorization{
		PlanID: result.Summary.PlanID, PlanHash: result.Summary.PlanHash,
		EstimateVersion: result.Estimate.Version, ReservationID: result.Reservation.ReservationID,
		Target: "production", ActorID: "agent-1", ExpiresAt: now.Add(10 * time.Minute),
	}
	if _, err = service.AuthorizeApply(context.Background(), infraapp.ApplyCommand{TenantID: "tenant-1", ProjectID: "project-1", IdempotencyKey: "apply-1", Authorization: authorization}); !errors.Is(err, infraapp.ErrApprovalRequired) {
		t.Fatalf("apply without approval was not rejected: %v", err)
	}
	grant, err := service.GrantApproval(context.Background(), infraapp.GrantApprovalCommand{
		TenantID: "tenant-1", ProjectID: "project-1", PlanID: result.Summary.PlanID,
		ActorID: "agent-1", ApproverUserID: "human-1", ExpiresAt: now.Add(15 * time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	authorization.ApprovalGrantID = grant.GrantID
	mismatch := authorization
	mismatch.ActorID = "agent-2"
	if _, err = service.AuthorizeApply(context.Background(), infraapp.ApplyCommand{TenantID: "tenant-1", ProjectID: "project-1", IdempotencyKey: "apply-1", Authorization: mismatch}); !errors.Is(err, infraapp.ErrPermissionDenied) {
		t.Fatalf("approval was not bound to exact actor: %v", err)
	}
	started, err := service.AuthorizeApply(context.Background(), infraapp.ApplyCommand{TenantID: "tenant-1", ProjectID: "project-1", IdempotencyKey: "apply-1", Authorization: authorization})
	if err != nil || started.ApplyStartedAt.IsZero() {
		t.Fatalf("matching authorization rejected: record=%#v err=%v", started, err)
	}
	if replayed, replayErr := service.AuthorizeApply(context.Background(), infraapp.ApplyCommand{TenantID: "tenant-1", ProjectID: "project-1", IdempotencyKey: "apply-1", Authorization: authorization}); replayErr != nil || replayed.ApplyStartedAt != started.ApplyStartedAt {
		t.Fatalf("exact apply retry was not idempotent: record=%#v err=%v", replayed, replayErr)
	}
	if _, err = service.AuthorizeApply(context.Background(), infraapp.ApplyCommand{TenantID: "tenant-1", ProjectID: "project-1", IdempotencyKey: "different-apply", Authorization: authorization}); !errors.Is(err, infraapp.ErrConflict) {
		t.Fatalf("single-use approval was reused by another command: %v", err)
	}
}

func TestAgent_DestroyWorkflowShowsPlanAndRetainsResourcesByPolicy(t *testing.T) {
	service, store, now := infrastructureFixture()
	planJSON := []byte(`{
		"resource_changes":[
			{"address":"twc_server.web","provider_name":"registry.opentofu.org/timeweb-cloud/timeweb-cloud","type":"twc_server","change":{"actions":["delete"]}}
		]
	}`)
	scope := workspace.PlanReceiptScope{
		TenantID: "tenant-1", ProjectID: "project-1", WorkspaceID: "workspace-1",
		TaskID: "task-destroy", CommandID: "command-destroy", ActorID: "agent-1",
	}
	receipt := infrastructurev1.AgentPlanReceipt{
		SessionID: "session-destroy", ExecutionSessionID: "session-destroy", CommandID: scope.CommandID,
		ArtifactDigest: "sha256:" + strings.Repeat("b", 64), PlanJSON: planJSON, CapturedAt: now,
		RetainedResources: []infrastructurev1.RetainedResource{{
			Address: "cozystack_postgres.primary", Provider: "cozystack", ResourceType: "cozystack_postgres",
			ExternalID: "postgres-primary", Policy: "platform.yaml/v2:retain", Reason: "production data retention policy",
		}},
	}
	if err := store.PutPlanReceipt(context.Background(), scope, receipt); err != nil {
		t.Fatal(err)
	}
	changedReceipt := receipt
	changedReceipt.RetainedResources = append([]infrastructurev1.RetainedResource(nil), receipt.RetainedResources...)
	changedReceipt.RetainedResources[0].Reason = "attacker changed retention decision"
	if err := store.PutPlanReceipt(context.Background(), scope, changedReceipt); !errors.Is(err, infraapp.ErrConflict) {
		t.Fatalf("receipt replay changed the retention decision: %v", err)
	}
	result, err := service.PlanFromReceipt(context.Background(), infraapp.ReceiptPlanCommand{
		TenantID: "tenant-1", ProjectID: "project-1", ActorID: "agent-1", WorkspaceID: "workspace-1",
		CommandID: scope.CommandID, Target: "production", SourceSHA: strings.Repeat("a", 40),
		IdempotencyKey: "destroy-project", StateGeneration: 7,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Summary.Destructive || !result.Summary.RequiresApproval || len(result.Summary.Changes) != 1 || result.Summary.Changes[0].Action != infrastructurev1.ActionDelete || len(result.Summary.RetainedResources) != 1 {
		t.Fatalf("destroy plan=%+v", result.Summary)
	}
	hashScope := scope
	hashScope.CommandID = "command-destroy-other-retention"
	hashReceipt := receipt
	hashReceipt.CommandID = hashScope.CommandID
	hashReceipt.RetainedResources = append([]infrastructurev1.RetainedResource(nil), receipt.RetainedResources...)
	hashReceipt.RetainedResources[0].Reason = "different approved retention basis"
	if err := store.PutPlanReceipt(context.Background(), hashScope, hashReceipt); err != nil {
		t.Fatal(err)
	}
	differentRetention, err := service.PlanFromReceipt(context.Background(), infraapp.ReceiptPlanCommand{
		TenantID: "tenant-1", ProjectID: "project-1", ActorID: "agent-1", WorkspaceID: "workspace-1",
		CommandID: hashScope.CommandID, Target: "production", SourceSHA: strings.Repeat("a", 40),
		IdempotencyKey: "destroy-project-different-retention", StateGeneration: 7,
	})
	if err != nil || differentRetention.Summary.PlanHash == result.Summary.PlanHash {
		t.Fatalf("retention decision was not bound into canonical plan hash: plan=%+v error=%v", differentRetention.Summary, err)
	}
	summary, err := service.GetApprovalSummary(context.Background(), "tenant-1", "project-1", result.Summary.PlanID)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Counts.Delete != 1 || summary.Counts.Retain != 1 || len(summary.RetainedResources) != 1 || summary.RetainedResources[0].Address != "cozystack_postgres.primary" {
		t.Fatalf("human destroy summary=%+v", summary)
	}
	grant, err := service.GrantApproval(context.Background(), infraapp.GrantApprovalCommand{
		TenantID: "tenant-1", ProjectID: "project-1", PlanID: result.Summary.PlanID,
		ActorID: "agent-1", ApproverUserID: "human-1", ExpiresAt: now.Add(15 * time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	authorized, err := service.AuthorizeApply(context.Background(), infraapp.ApplyCommand{
		TenantID: "tenant-1", ProjectID: "project-1", IdempotencyKey: "destroy-apply",
		Authorization: infrastructurev1.ApplyAuthorization{
			PlanID: result.Summary.PlanID, PlanHash: result.Summary.PlanHash,
			EstimateVersion: result.Estimate.Version, ReservationID: result.Reservation.ReservationID,
			ApprovalGrantID: grant.GrantID, Target: "production", ActorID: "agent-1", ExpiresAt: now.Add(10 * time.Minute),
		},
	})
	if err != nil || authorized.ApplyStartedAt.IsZero() {
		t.Fatalf("authorized destroy=%+v error=%v", authorized, err)
	}
	for _, change := range authorized.Summary.Changes {
		if change.Address == "cozystack_postgres.primary" {
			t.Fatalf("retained stateful resource entered executable delete set: %+v", change)
		}
	}

	conflictScope := scope
	conflictScope.CommandID = "command-destroy-conflict"
	conflictReceipt := receipt
	conflictReceipt.CommandID = conflictScope.CommandID
	conflictReceipt.ArtifactDigest = "sha256:" + strings.Repeat("e", 64)
	conflictReceipt.PlanJSON = []byte(`{
		"resource_changes":[
			{"address":"cozystack_postgres.primary","provider_name":"cozystack","type":"cozystack_postgres","change":{"actions":["delete"]}}
		]
	}`)
	if err := store.PutPlanReceipt(context.Background(), conflictScope, conflictReceipt); err != nil {
		t.Fatal(err)
	}
	if _, err := service.PlanFromReceipt(context.Background(), infraapp.ReceiptPlanCommand{
		TenantID: "tenant-1", ProjectID: "project-1", ActorID: "agent-1", WorkspaceID: "workspace-1",
		CommandID: conflictScope.CommandID, Target: "production", SourceSHA: strings.Repeat("a", 40),
		IdempotencyKey: "destroy-policy-conflict", StateGeneration: 7,
	}); err == nil {
		t.Fatal("contradictory DELETE and RETAIN policy was accepted")
	}
}
