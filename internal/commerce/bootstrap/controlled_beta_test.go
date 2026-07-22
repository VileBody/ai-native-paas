package bootstrap_test

import (
	"context"
	"testing"
	"time"

	"github.com/keir-research/ai-native-paas/internal/commerce/application"
	"github.com/keir-research/ai-native-paas/internal/commerce/bootstrap"
	"github.com/keir-research/ai-native-paas/internal/commerce/memory"
	"github.com/keir-research/ai-native-paas/internal/commerce/testkit"
	commercev1 "github.com/keir-research/ai-native-paas/pkg/contracts/commerce/v1"
)

func TestEnsureControlledBetaIsIdempotentAndUsable(t *testing.T) {
	store := memory.New()
	now := time.Date(2026, 7, 22, 20, 0, 0, 0, time.UTC)
	service := &application.Service{Store: store, Clock: &testkit.Clock{T: now}, IDs: &testkit.IDs{}, Ownership: application.DenyAllOwnership{}}
	config := bootstrap.ControlledBetaConfig{
		TenantID: "tenant-live", SubscriptionID: "sub-live", BillingPeriodID: "period-live",
		WorkspaceSeconds: 3600, Now: now,
	}
	for range 2 {
		if err := bootstrap.EnsureControlledBeta(context.Background(), service, store, config); err != nil {
			t.Fatal(err)
		}
	}
	reservation, err := service.Reserve(context.Background(), commercev1.QuotaRequest{
		TenantID: "tenant-live", ProjectID: "project-live", Resource: "workspace.command_seconds",
		Quantity: 60, IdempotencyKey: "seed-test", At: now, ExpiresAt: now.Add(time.Hour),
	})
	if err != nil || reservation.PolicyVersion != bootstrap.ControlledBetaPolicyVersion {
		t.Fatalf("reservation=%+v err=%v", reservation, err)
	}
}

func TestEnsureControlledBetaRejectsChangedActiveQuota(t *testing.T) {
	store := memory.New()
	now := time.Date(2026, 7, 22, 20, 0, 0, 0, time.UTC)
	service := &application.Service{Store: store, Clock: &testkit.Clock{T: now}, IDs: &testkit.IDs{}, Ownership: application.DenyAllOwnership{}}
	config := bootstrap.ControlledBetaConfig{TenantID: "tenant-live", SubscriptionID: "sub-live", BillingPeriodID: "period-live", WorkspaceSeconds: 3600, Now: now}
	if err := bootstrap.EnsureControlledBeta(context.Background(), service, store, config); err != nil {
		t.Fatal(err)
	}
	config.WorkspaceSeconds++
	if err := bootstrap.EnsureControlledBeta(context.Background(), service, store, config); err == nil {
		t.Fatal("changed immutable beta quota was accepted")
	}
}
