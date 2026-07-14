package pivot_test

import (
	"context"
	"errors"
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

func attachmentHasCode(err error, code domain.Code) bool {
	var typed *domain.Error
	return errors.As(err, &typed) && typed.Code == code
}
