package pivot_test

import (
	"context"
	"errors"
	"testing"
	"time"

	infraapp "github.com/keir-research/ai-native-paas/internal/infrastructure/application"
	infrastructurev1 "github.com/keir-research/ai-native-paas/pkg/contracts/infrastructure/v1"
)

func TestCost_TofuPlanProducesDeterministicNormalizedEstimate(t *testing.T) {
	service, _, _ := infrastructureFixture()
	first, err := service.Plan(context.Background(), planCommand("estimate-a", "staging", twoResourcePlan))
	if err != nil {
		t.Fatal(err)
	}
	reordered := `{"resource_changes":[
    {"type":"unknown_cache","provider_name":"example/unknown","address":"unknown_cache.app","change":{"actions":["create"]}},
    {"type":"twc_server","provider_name":"registry.opentofu.org/timeweb-cloud/timeweb-cloud","address":"twc_server.app","change":{"after":{"different":"ignored"},"actions":["create"]}}
  ],"terraform_version":"1.12.4","format_version":"1.2"}`
	second, err := service.Plan(context.Background(), planCommand("estimate-b", "staging", reordered))
	if err != nil {
		t.Fatal(err)
	}
	if first.Summary.PlanHash != second.Summary.PlanHash || first.Estimate.Version != second.Estimate.Version {
		t.Fatalf("normalized plan/estimate changed with input ordering: first=%#v second=%#v", first, second)
	}
	if first.Estimate.Minimum.MinorUnit != 125 || first.Estimate.Maximum.MinorUnit != 1_250_125 || !first.Estimate.ApprovalRequired {
		t.Fatalf("unexpected conservative 25%% estimate: %#v", first.Estimate)
	}
	if len(first.Estimate.Lines) != 2 || first.Estimate.Lines[0].Meter != "timeweb.server.month" || first.Estimate.Lines[1].PriceKnown {
		t.Fatalf("estimate lines are not canonical/provider-aware: %#v", first.Estimate.Lines)
	}
}

func TestCost_ApprovalInvalidatedWhenPlanHashChanges(t *testing.T) {
	service, _, now := infrastructureFixture()
	planA, err := service.Plan(context.Background(), planCommand("approval-plan-a", "production", knownResourcePlan))
	if err != nil {
		t.Fatal(err)
	}
	grantA, err := service.GrantApproval(context.Background(), infraapp.GrantApprovalCommand{
		TenantID: "tenant-1", ProjectID: "project-1", PlanID: planA.Summary.PlanID,
		ActorID: "agent-1", ApproverUserID: "human-1", ExpiresAt: now.Add(15 * time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	changed := `{"resource_changes":[{"address":"twc_server.changed","provider_name":"registry.opentofu.org/timeweb-cloud/timeweb-cloud","type":"twc_server","change":{"actions":["create"]}}]}`
	planB, err := service.Plan(context.Background(), planCommand("approval-plan-b", "production", changed))
	if err != nil {
		t.Fatal(err)
	}
	if planA.Summary.PlanHash == planB.Summary.PlanHash {
		t.Fatal("IaC change did not produce a new canonical plan hash")
	}
	authorization := infrastructurev1.ApplyAuthorization{
		PlanID: planB.Summary.PlanID, PlanHash: planB.Summary.PlanHash,
		EstimateVersion: planB.Estimate.Version, ReservationID: planB.Reservation.ReservationID,
		ApprovalGrantID: grantA.GrantID, Target: "production", ActorID: "agent-1",
		ExpiresAt: now.Add(10 * time.Minute),
	}
	if _, err = service.AuthorizeApply(context.Background(), infraapp.ApplyCommand{
		TenantID: "tenant-1", ProjectID: "project-1", IdempotencyKey: "apply-plan-b", Authorization: authorization,
	}); !errors.Is(err, infraapp.ErrPermissionDenied) {
		t.Fatalf("approval for plan A authorized plan B: %v", err)
	}
}

func TestQuota_ExpiredReservationCannotAuthorizeLateApply(t *testing.T) {
	service, store, now := infrastructureFixture()
	result, err := service.Plan(context.Background(), planCommand("expiring-reservation", "staging", knownResourcePlan))
	if err != nil {
		t.Fatal(err)
	}
	if result.Summary.RequiresApproval {
		t.Fatal("known-price staging plan unexpectedly requires approval")
	}
	service.Clock = fixedClock{now: now.Add(21 * time.Minute)}
	authorization := infrastructurev1.ApplyAuthorization{
		PlanID: result.Summary.PlanID, PlanHash: result.Summary.PlanHash,
		EstimateVersion: result.Estimate.Version, ReservationID: result.Reservation.ReservationID,
		Target: "staging", ActorID: "agent-1", ExpiresAt: now.Add(30 * time.Minute),
	}
	if _, err = service.AuthorizeApply(context.Background(), infraapp.ApplyCommand{
		TenantID: "tenant-1", ProjectID: "project-1", IdempotencyKey: "late-apply", Authorization: authorization,
	}); !errors.Is(err, infraapp.ErrPermissionDenied) {
		t.Fatalf("expired reservation authorized apply: %v", err)
	}
	stored, err := store.GetPlan(context.Background(), "tenant-1", "project-1", result.Summary.PlanID)
	if err != nil {
		t.Fatal(err)
	}
	if !stored.ApplyStartedAt.IsZero() {
		t.Fatalf("rejected late apply was marked started: %#v", stored)
	}
}

const knownResourcePlan = `{
  "resource_changes":[
    {"address":"twc_server.app","provider_name":"registry.opentofu.org/timeweb-cloud/timeweb-cloud","type":"twc_server","change":{"actions":["create"]}}
  ]
}`
