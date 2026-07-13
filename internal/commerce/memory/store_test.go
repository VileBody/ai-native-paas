package memory_test

import (
	"context"
	"testing"
	"time"

	"github.com/keir-research/ai-native-paas/internal/commerce/application"
	"github.com/keir-research/ai-native-paas/internal/commerce/domain"
	"github.com/keir-research/ai-native-paas/internal/commerce/memory"
	commercev1 "github.com/keir-research/ai-native-paas/pkg/contracts/commerce/v1"
)

func TestStore_ClonesMutablePlanSpecAtTransactionBoundary(t *testing.T) {
	store := memory.New()
	now := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	definition, _ := domain.NewPlanDefinition("plan", "Plan", now)
	spec := commercev1.PlanSpec{Currency: "EUR", Features: map[string]bool{"deploy": true}, Quotas: map[string]int64{"runtime.units": 2}, Prices: map[commercev1.Meter]commercev1.Price{commercev1.MeterRuntimeUnitSeconds: {MinorUnits: 1, PerQuantity: 3600}}}
	version, _ := domain.NewPlanVersion("plan-v1", definition.ID, "policy-v1", 1, spec, now, now)
	if err := store.Transact(context.Background(), func(tx application.Tx) error {
		if err := tx.InsertPlanDefinition(definition); err != nil {
			return err
		}
		return tx.InsertPlanVersion(version)
	}); err != nil {
		t.Fatal(err)
	}
	spec.Quotas["runtime.units"] = 99
	version.Spec.Features["deploy"] = false
	if err := store.Transact(context.Background(), func(tx application.Tx) error {
		persisted, ok := tx.GetPlanVersion("plan-v1")
		if !ok {
			t.Fatal("missing")
		}
		if persisted.Spec.Quotas["runtime.units"] != 2 || !persisted.Spec.Features["deploy"] {
			t.Fatalf("persisted spec mutated: %+v", persisted.Spec)
		}
		persisted.Spec.Quotas["runtime.units"] = 77
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Transact(context.Background(), func(tx application.Tx) error {
		persisted, _ := tx.GetPlanVersion("plan-v1")
		if persisted.Spec.Quotas["runtime.units"] != 2 {
			t.Fatalf("read alias mutated store: %+v", persisted.Spec)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
