package acceptance_test

import (
	"context"
	"errors"
	"testing"

	"github.com/keir-research/ai-native-paas/internal/attachments/application"
	"github.com/keir-research/ai-native-paas/internal/attachments/domain"
	"github.com/keir-research/ai-native-paas/internal/attachments/memory"
	"github.com/keir-research/ai-native-paas/internal/attachments/testkit"
	attachmentsv1 "github.com/keir-research/ai-native-paas/pkg/contracts/attachments/v1"
)

func commerceFixture(t *testing.T) (*application.Service, *testkit.Provider, *testkit.Commerce, *testkit.Usage) {
	t.Helper()
	clock := testkit.NewClock()
	provider := &testkit.Provider{EnsureResult: application.ProviderInstanceResult{ProviderID: "provider-1", Ready: true}}
	commerce := &testkit.Commerce{Allowed: true}
	usage := &testkit.Usage{}
	service := &application.Service{Store: memory.New(), Provider: provider, Commerce: commerce, Usage: usage, Clock: clock, IDs: &application.SequentialIDs{}}
	plan, err := domain.NewServicePlan("pg-small", 1, attachmentsv1.ServicePostgreSQL, "cozystack", "small", "mapping-v1", false, []string{"read"}, true, true, clock.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := service.RegisterServicePlan(context.Background(), plan, "system"); err != nil {
		t.Fatal(err)
	}
	return service, provider, commerce, usage
}

func provisionForCommerce(service *application.Service, key string) (domain.ServiceInstance, error) {
	return service.ProvisionService(context.Background(), application.ProvisionServiceRequest{
		TenantID: "tenant-1", Name: "primary-" + key, PlanID: "pg-small", ActorID: "user-1", IdempotencyKey: key,
	})
}

func TestAttachments_CommerceEntitlementCheckedBeforeProviderCreate(t *testing.T) {
	service, provider, commerce, _ := commerceFixture(t)
	commerce.Allowed = false
	if _, err := provisionForCommerce(service, "denied"); err == nil {
		t.Fatal("allocation without entitlement was accepted")
	}
	if commerce.Calls != 1 || provider.EnsureCalls != 0 {
		t.Fatalf("entitlement calls=%d provider calls=%d", commerce.Calls, provider.EnsureCalls)
	}
}

func TestAttachments_CommerceQuotaRejectedBeforeProviderSideEffect(t *testing.T) {
	service, provider, commerce, usage := commerceFixture(t)
	commerce.Allowed = false
	if _, err := provisionForCommerce(service, "quota-denied"); err == nil {
		t.Fatal("quota denial was ignored")
	}
	if provider.EnsureCalls != 0 || usage.Count() != 0 {
		t.Fatalf("provider calls=%d usage=%d", provider.EnsureCalls, usage.Count())
	}
}

func TestAttachments_UsageEmittedOnlyAfterAllocationAccepted(t *testing.T) {
	service, provider, _, usage := commerceFixture(t)
	provider.EnsureErr = errors.New("provider unavailable")
	if _, err := provisionForCommerce(service, "failed"); err == nil {
		t.Fatal("provider failure was hidden")
	}
	if usage.Count() != 0 {
		t.Fatalf("usage emitted for rejected allocation: %d", usage.Count())
	}

	service, _, _, usage = commerceFixture(t)
	instance, err := provisionForCommerce(service, "accepted")
	if err != nil {
		t.Fatal(err)
	}
	if usage.Count() != 1 || usage.Events[0].ResourceID != instance.ID {
		t.Fatalf("usage=%+v instance=%s", usage.Events, instance.ID)
	}
}
