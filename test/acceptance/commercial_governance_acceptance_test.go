package acceptance_test

import (
	"context"
	"testing"
	"time"

	"github.com/keir-research/ai-native-paas/internal/commerce/application"
	"github.com/keir-research/ai-native-paas/internal/commerce/domain"
	"github.com/keir-research/ai-native-paas/internal/commerce/memory"
	"github.com/keir-research/ai-native-paas/internal/commerce/testkit"
	commercev1 "github.com/keir-research/ai-native-paas/pkg/contracts/commerce/v1"
)

func TestCommercialGovernance_Acceptance(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 1, 1, 0, 0, 0, time.UTC)
	clock := &testkit.Clock{T: now}
	store := memory.New()
	svc := &application.Service{Store: store, Clock: clock, IDs: &testkit.IDs{}, Ownership: application.AllowAllOwnership{}, DriftAlertThreshold: 30}
	spec := commercev1.PlanSpec{
		Currency: "EUR",
		Features: map[string]bool{"deploy": true, "managed.postgres": true},
		Quotas:   map[string]int64{"runtime.units": 2, "managed.databases": 1},
		Prices: map[commercev1.Meter]commercev1.Price{
			commercev1.MeterRuntimeUnitSeconds:  {MinorUnits: 1, PerQuantity: 3600},
			commercev1.MeterDatabasePlanSeconds: {MinorUnits: 10, PerQuantity: 86400},
		},
		Included: map[commercev1.Meter]int64{},
	}
	if _, err := svc.CreatePlanDefinition(ctx, application.CreatePlanDefinitionCommand{ID: "developer", Name: "Developer"}); err != nil {
		t.Fatal(err)
	}
	plan, err := svc.CreatePlanVersion(ctx, application.CreatePlanVersionCommand{ID: "developer-v1", DefinitionID: "developer", PolicyVersion: "policy-v1", Number: 1, Spec: spec, EffectiveFrom: now.Add(-time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	plan, err = svc.ActivatePlanVersion(ctx, plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, period, err := svc.StartSubscription(ctx, application.StartSubscriptionCommand{ID: "sub-1", TenantID: "tenant-1", PlanVersionID: plan.ID, PeriodID: "period-1", State: domain.SubscriptionActive, PeriodStart: now.Add(-time.Hour), PeriodEnd: now.Add(30 * 24 * time.Hour)})
	if err != nil {
		t.Fatal(err)
	}

	entitlement, err := svc.Check(ctx, commercev1.EntitlementRequest{TenantID: "tenant-1", Feature: "deploy", Resource: "runtime.units", Quantity: 2, At: now})
	if err != nil || !entitlement.Allowed || entitlement.Remaining != 2 {
		t.Fatalf("entitlement=%+v err=%v", entitlement, err)
	}

	first, err := svc.Reserve(ctx, commercev1.QuotaRequest{TenantID: "tenant-1", Resource: "runtime.units", Quantity: 1, IdempotencyKey: "app-1", At: now, ExpiresAt: now.Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	second, err := svc.Reserve(ctx, commercev1.QuotaRequest{TenantID: "tenant-1", Resource: "runtime.units", Quantity: 1, IdempotencyKey: "app-2", At: now, ExpiresAt: now.Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Commit(ctx, first.ID); err != nil {
		t.Fatal(err)
	}
	if err := svc.Commit(ctx, second.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Reserve(ctx, commercev1.QuotaRequest{TenantID: "tenant-1", Resource: "runtime.units", Quantity: 1, IdempotencyKey: "app-3", At: now, ExpiresAt: now.Add(time.Hour)}); !domain.HasCode(err, domain.CodeQuotaExceeded) {
		t.Fatalf("third reservation err=%v", err)
	}

	usage := commercev1.UsageEvent{TenantID: "tenant-1", PeriodID: period.ID, ResourceType: "application", ResourceID: "app-1", Meter: commercev1.MeterRuntimeUnitSeconds, Quantity: 3600, IdempotencyKey: "runtime-hour-1", OccurredAt: now, WindowStart: now.Add(-time.Hour), WindowEnd: now}
	if err := svc.Append(ctx, usage); err != nil {
		t.Fatal(err)
	}
	if err := svc.Append(ctx, usage); err != nil {
		t.Fatalf("duplicate usage must be idempotent: %v", err)
	}
	preview, err := svc.PreviewInvoice(ctx, "tenant-1", period.ID)
	if err != nil {
		t.Fatal(err)
	}
	if preview.TotalMinorUnits != 1 || len(preview.Lines) != 1 {
		t.Fatalf("preview=%+v", preview)
	}

	intent, err := svc.Suspend(ctx, "tenant-1")
	if err != nil {
		t.Fatal(err)
	}
	if intent.Action != "suspend" || !intent.RetainManagedServices {
		t.Fatalf("intent=%+v", intent)
	}
	denied, err := svc.Check(ctx, commercev1.EntitlementRequest{TenantID: "tenant-1", Feature: "deploy", At: now})
	if err != nil || denied.Allowed {
		t.Fatalf("suspended decision=%+v err=%v", denied, err)
	}
	resume, err := svc.Resume(ctx, "tenant-1")
	if err != nil {
		t.Fatal(err)
	}
	if resume.Action != "resume" || !resume.RetainManagedServices {
		t.Fatalf("resume=%+v", resume)
	}

	usageCount, quotaCount, outboxCount, auditCount, _ := store.SnapshotCounts()
	if usageCount != 1 || quotaCount != 2 || outboxCount == 0 || auditCount == 0 {
		t.Fatalf("counts usage=%d quota=%d outbox=%d audit=%d", usageCount, quotaCount, outboxCount, auditCount)
	}
}
