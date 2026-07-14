package pivot_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
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

func TestCost_ApprovalSummaryIncludesDestructionRiskAndMonthlyDelta(t *testing.T) {
	service, _, _ := infrastructureFixture()
	plan := `{"resource_changes":[
		{"address":"twc_server.new","provider_name":"timeweb","type":"twc_server","change":{"actions":["create"]}},
		{"address":"twc_server.changed","provider_name":"timeweb","type":"twc_server","change":{"actions":["update"]}},
		{"address":"twc_server.old","provider_name":"timeweb","type":"twc_server","change":{"actions":["delete"]}}
	]}`
	result, err := service.Plan(context.Background(), planCommand("approval-summary", "production", plan))
	if err != nil {
		t.Fatal(err)
	}
	summary, err := service.GetApprovalSummary(context.Background(), "tenant-1", "project-1", result.Summary.PlanID)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.MarshalIndent(summary, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	const golden = `{
  "plan_id": "plan-2",
  "project_id": "project-1",
  "target": "production",
  "plan_hash": "PLAN_HASH",
  "counts": {
    "create": 1,
    "update": 1,
    "replace": 0,
    "delete": 1,
    "no_op": 0
  },
  "destruction_risks": [
    {
      "address": "twc_server.old",
      "action": "DELETE",
      "reason": "resource deletion is irreversible"
    }
  ],
  "monthly_delta": {
    "currency": "RUB",
    "minimum_minor": -125,
    "maximum_minor": 125,
    "complete": false
  },
  "one_time_delta": {
    "currency": "RUB",
    "minimum_minor": 0,
    "maximum_minor": 0,
    "complete": false
  },
  "unknowns": [
    "monthly-delta:twc_server.changed",
    "one-time-price:twc_server.changed",
    "one-time-price:twc_server.new"
  ],
  "estimate_version": "ESTIMATE_VERSION",
  "reservation_id": "reservation-3",
  "approval_expires_at": "2026-07-14T09:50:00Z"
}`
	want := strings.ReplaceAll(golden, "PLAN_HASH", result.Summary.PlanHash)
	want = strings.ReplaceAll(want, "ESTIMATE_VERSION", result.Estimate.Version)
	if string(raw) != want {
		t.Fatalf("approval summary contract changed:\n%s\nwant:\n%s", raw, want)
	}
}

const knownResourcePlan = `{
  "resource_changes":[
    {"address":"twc_server.app","provider_name":"registry.opentofu.org/timeweb-cloud/timeweb-cloud","type":"twc_server","change":{"actions":["create"]}}
  ]
}`
