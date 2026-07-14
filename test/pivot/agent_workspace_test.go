package pivot_test

import (
	"errors"
	"testing"
	"time"

	infraapp "github.com/keir-research/ai-native-paas/internal/infrastructure/application"
	infrastructurev1 "github.com/keir-research/ai-native-paas/pkg/contracts/infrastructure/v1"
)

func TestInfraPlan_IsPureIdempotentAndStoresCanonicalPlanHash(t *testing.T) {
	service, _, _ := infrastructureFixture()
	first, err := service.Plan(planCommand("same-command", "staging", twoResourcePlan))
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.Plan(planCommand("same-command", "staging", twoResourcePlan))
	if err != nil {
		t.Fatal(err)
	}
	if first.Summary.PlanID != second.Summary.PlanID || first.Summary.PlanHash != second.Summary.PlanHash || first.Reservation.ReservationID != second.Reservation.ReservationID {
		t.Fatalf("idempotent plan changed durable identity: first=%#v second=%#v", first, second)
	}
	changed := planCommand("same-command", "staging", twoResourcePlan)
	changed.ArtifactDigest = "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	if _, err = service.Plan(changed); !errors.Is(err, infraapp.ErrConflict) {
		t.Fatalf("idempotency payload mismatch was accepted: %v", err)
	}
}

func TestInfraApply_RequiresMatchingPlanReservationAndApproval(t *testing.T) {
	service, _, now := infrastructureFixture()
	result, err := service.Plan(planCommand("production-plan", "production", twoResourcePlan))
	if err != nil {
		t.Fatal(err)
	}
	authorization := infrastructurev1.ApplyAuthorization{
		PlanID: result.Summary.PlanID, PlanHash: result.Summary.PlanHash,
		EstimateVersion: result.Estimate.Version, ReservationID: result.Reservation.ReservationID,
		Target: "production", ActorID: "agent-1", ExpiresAt: now.Add(10 * time.Minute),
	}
	if _, err = service.AuthorizeApply(infraapp.ApplyCommand{TenantID: "tenant-1", ProjectID: "project-1", Authorization: authorization}); !errors.Is(err, infraapp.ErrApprovalRequired) {
		t.Fatalf("apply without approval was not rejected: %v", err)
	}
	grant, err := service.GrantApproval(infraapp.GrantApprovalCommand{
		TenantID: "tenant-1", ProjectID: "project-1", PlanID: result.Summary.PlanID,
		ActorID: "agent-1", ApproverUserID: "human-1", ExpiresAt: now.Add(15 * time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	authorization.ApprovalGrantID = grant.GrantID
	mismatch := authorization
	mismatch.ActorID = "agent-2"
	if _, err = service.AuthorizeApply(infraapp.ApplyCommand{TenantID: "tenant-1", ProjectID: "project-1", Authorization: mismatch}); !errors.Is(err, infraapp.ErrPermissionDenied) {
		t.Fatalf("approval was not bound to exact actor: %v", err)
	}
	started, err := service.AuthorizeApply(infraapp.ApplyCommand{TenantID: "tenant-1", ProjectID: "project-1", Authorization: authorization})
	if err != nil || started.ApplyStartedAt.IsZero() {
		t.Fatalf("matching authorization rejected: record=%#v err=%v", started, err)
	}
	if _, err = service.AuthorizeApply(infraapp.ApplyCommand{TenantID: "tenant-1", ProjectID: "project-1", Authorization: authorization}); !errors.Is(err, infraapp.ErrConflict) {
		t.Fatalf("single-use approval/apply was reused: %v", err)
	}
}
