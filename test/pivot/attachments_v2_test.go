package pivot_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/keir-research/ai-native-paas/internal/attachments/application"
	"github.com/keir-research/ai-native-paas/internal/attachments/domain"
	"github.com/keir-research/ai-native-paas/internal/attachments/memory"
	"github.com/keir-research/ai-native-paas/internal/attachments/testkit"
	attachmentsv1 "github.com/keir-research/ai-native-paas/pkg/contracts/attachments/v1"
)

func TestDomain_ReleaseEntersQuarantineBeforeAnotherTenantCanClaim(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	clock := testkit.NewClock()
	quarantine := time.Hour
	service := &application.Service{
		Store: memory.New(),
		Environments: &testkit.Environments{Values: map[string]application.EnvironmentRef{
			"env-a": {TenantID: "tenant-a", ApplicationID: "app-a", EnvironmentID: "env-a", Ready: true},
			"env-b": {TenantID: "tenant-b", ApplicationID: "app-b", EnvironmentID: "env-b", Ready: true},
		}},
		Clock:            clock,
		IDs:              &application.SequentialIDs{},
		DomainQuarantine: quarantine,
	}

	claim, err := service.ClaimCustomDomain(ctx, application.ClaimDomainRequest{
		TenantID:       "tenant-a",
		ApplicationID:  "app-a",
		EnvironmentID:  "env-a",
		Hostname:       "shared.example.test",
		ActorID:        "user-a",
		IdempotencyKey: "claim-a",
	})
	if err != nil {
		t.Fatalf("tenant A claim: %v", err)
	}

	released, err := service.DeleteDomain(ctx, application.DeleteDomainRequest{
		TenantID: "tenant-a",
		ClaimID:  claim.ID,
		ActorID:  "user-a",
	})
	if err != nil {
		t.Fatalf("release domain: %v", err)
	}
	if released.State != attachmentsv1.DomainQuarantined || !released.QuarantineUntil.Equal(clock.Now().Add(quarantine)) {
		t.Fatalf("release must enter a fixed quarantine: %+v", released)
	}

	claimAsTenantB := func(key string) (domain.DomainClaim, error) {
		return service.ClaimCustomDomain(ctx, application.ClaimDomainRequest{
			TenantID:       "tenant-b",
			ApplicationID:  "app-b",
			EnvironmentID:  "env-b",
			Hostname:       claim.Hostname,
			ActorID:        "user-b",
			IdempotencyKey: key,
		})
	}
	if _, err = claimAsTenantB("claim-b-too-soon"); !attachmentHasCode(err, domain.CodeConflict) {
		t.Fatalf("another tenant claimed a quarantined domain: %v", err)
	}

	clock.Advance(quarantine - time.Nanosecond)
	if _, err = service.ReleaseDomain(ctx, "tenant-a", claim.ID, "system"); !attachmentHasCode(err, domain.CodeConflict) {
		t.Fatalf("quarantine ended before its exact deadline: %v", err)
	}

	clock.Advance(time.Nanosecond)
	if _, err = service.ReleaseDomain(ctx, "tenant-a", claim.ID, "system"); err != nil {
		t.Fatalf("release after quarantine: %v", err)
	}

	newClaim, err := claimAsTenantB("claim-b-after-quarantine")
	if err != nil {
		t.Fatalf("tenant B claim after quarantine: %v", err)
	}
	if newClaim.State != attachmentsv1.DomainAwaitingVerification || newClaim.ChallengeValue == "" {
		t.Fatalf("new tenant bypassed ownership proof: %+v", newClaim)
	}
	if _, err = service.RouteForEnvironment(ctx, "tenant-b", "env-b"); !attachmentHasCode(err, domain.CodeNotFound) {
		t.Fatalf("unverified replacement claim became routable: %v", err)
	}
}

func TestProviderResource_DestroyRequiresMatchingPlanHashApproval(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	clock := testkit.NewClock()
	provider := &testkit.Provider{EnsureResult: application.ProviderInstanceResult{
		ProviderID: "provider-resource-1",
		Endpoint:   "postgres.internal",
		Ready:      true,
	}}
	approvals := &testkit.Approvals{Grants: map[string]application.ApprovalBinding{}}
	service := &application.Service{
		Store:     memory.New(),
		Provider:  provider,
		Approvals: approvals,
		Commerce:  &testkit.Commerce{Allowed: true},
		Usage:     &testkit.Usage{},
		Clock:     clock,
		IDs:       &application.SequentialIDs{},
	}
	plan, err := domain.NewServicePlan(
		"postgres-small",
		1,
		attachmentsv1.ServicePostgreSQL,
		"cozystack",
		"small",
		"mapping-v1",
		false,
		[]string{"connect"},
		true,
		false,
		clock.Now(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err = service.RegisterServicePlan(ctx, plan, "system"); err != nil {
		t.Fatalf("register service plan: %v", err)
	}
	instance, err := service.ProvisionService(ctx, application.ProvisionServiceRequest{
		TenantID:       "tenant-a",
		Name:           "primary",
		PlanID:         plan.ID,
		PlanVersion:    plan.Version,
		ActorID:        "user-a",
		IdempotencyKey: "provision-primary",
	})
	if err != nil {
		t.Fatalf("provision provider resource: %v", err)
	}
	destroyPlan, err := domain.NewProviderResourceDestroyPlan(instance, plan)
	if err != nil {
		t.Fatal(err)
	}
	planHash := destroyPlan.PlanHash()
	grant := application.ApprovalBinding{
		TenantID: instance.TenantID,
		ActorID:  "user-a",
		TargetID: instance.ID,
		PlanHash: planHash,
	}
	approvals.Grants["grant-hash-a"] = grant

	_, err = service.PurgeService(ctx, application.PurgeServiceRequest{
		TenantID:    instance.TenantID,
		InstanceID:  instance.ID,
		PlanHash:    "sha256:" + strings.Repeat("b", 64),
		ActorID:     grant.ActorID,
		ApprovalRef: "grant-hash-a",
	})
	if !attachmentHasCode(err, domain.CodeForbidden) || provider.DeleteCalls != 0 {
		t.Fatalf("plan B used approval A: err=%v delete_calls=%d", err, provider.DeleteCalls)
	}

	grant.TargetID = "another-resource"
	approvals.Grants["grant-wrong-target"] = grant
	_, err = service.PurgeService(ctx, application.PurgeServiceRequest{
		TenantID:    instance.TenantID,
		InstanceID:  instance.ID,
		PlanHash:    planHash,
		ActorID:     grant.ActorID,
		ApprovalRef: "grant-wrong-target",
	})
	if !attachmentHasCode(err, domain.CodeForbidden) || provider.DeleteCalls != 0 {
		t.Fatalf("approval for another target destroyed resource: err=%v delete_calls=%d", err, provider.DeleteCalls)
	}

	grant.TargetID = instance.ID
	approvals.Grants["grant-exact"] = grant
	deleted, err := service.PurgeService(ctx, application.PurgeServiceRequest{
		TenantID:    instance.TenantID,
		InstanceID:  instance.ID,
		PlanHash:    planHash,
		ActorID:     grant.ActorID,
		ApprovalRef: "grant-exact",
	})
	if err != nil || deleted.State != attachmentsv1.ServiceDeleted || provider.DeleteCalls != 1 {
		t.Fatalf("exact approval did not destroy once: resource=%+v err=%v delete_calls=%d", deleted, err, provider.DeleteCalls)
	}
}

func attachmentHasCode(err error, code domain.Code) bool {
	var typed *domain.Error
	return errors.As(err, &typed) && typed.Code == code
}
