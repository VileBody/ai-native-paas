package application_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"math/rand"
	"sync"
	"testing"
	"time"

	"github.com/keir-research/ai-native-paas/internal/commerce/application"
	"github.com/keir-research/ai-native-paas/internal/commerce/domain"
	"github.com/keir-research/ai-native-paas/internal/commerce/memory"
	"github.com/keir-research/ai-native-paas/internal/commerce/testkit"
	commercev1 "github.com/keir-research/ai-native-paas/pkg/contracts/commerce/v1"
)

var baseTime = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

type fixture struct {
	ctx       context.Context
	store     *memory.Store
	clock     *testkit.Clock
	ids       *testkit.IDs
	ownership *testkit.Ownership
	svc       *application.Service
	plan      domain.PlanVersion
	period    domain.BillingPeriod
	sub       domain.Subscription
}

func planSpec() commercev1.PlanSpec {
	return commercev1.PlanSpec{
		Currency: "EUR",
		Features: map[string]bool{"deploy": true, "managed.postgres": true},
		Quotas:   map[string]int64{"runtime.units": 2, "managed.databases": 1},
		Prices: map[commercev1.Meter]commercev1.Price{
			commercev1.MeterRuntimeUnitSeconds:    {MinorUnits: 1, PerQuantity: 3600},
			commercev1.MeterBuildCPUSeconds:       {MinorUnits: 2, PerQuantity: 60},
			commercev1.MeterBuildMemoryGiBSeconds: {MinorUnits: 1, PerQuantity: 60},
			commercev1.MeterBuildDockerVMSeconds:  {MinorUnits: 5, PerQuantity: 60},
		},
		Included:                map[commercev1.Meter]int64{},
		ChargeUserBuildFailures: true,
	}
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	ctx := context.Background()
	store := memory.New()
	clock := &testkit.Clock{T: baseTime.Add(time.Hour)}
	ids := &testkit.IDs{}
	ownership := &testkit.Ownership{Allowed: map[string]bool{
		"tenant-1/application/app-1": true,
		"tenant-1/build/build-1":     true,
	}}
	svc := &application.Service{Store: store, Clock: clock, IDs: ids, Ownership: ownership, DriftAlertThreshold: 30}
	if _, err := svc.CreatePlanDefinition(ctx, application.CreatePlanDefinitionCommand{ID: "plan", Name: "Developer"}); err != nil {
		t.Fatal(err)
	}
	plan, err := svc.CreatePlanVersion(ctx, application.CreatePlanVersionCommand{ID: "plan-v1", DefinitionID: "plan", PolicyVersion: "policy-v1", Number: 1, Spec: planSpec(), EffectiveFrom: baseTime})
	if err != nil {
		t.Fatal(err)
	}
	plan, err = svc.ActivatePlanVersion(ctx, plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	sub, period, err := svc.StartSubscription(ctx, application.StartSubscriptionCommand{ID: "sub-1", TenantID: "tenant-1", PlanVersionID: plan.ID, PeriodID: "period-1", State: domain.SubscriptionActive, PeriodStart: baseTime, PeriodEnd: baseTime.Add(31 * 24 * time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	return &fixture{ctx: ctx, store: store, clock: clock, ids: ids, ownership: ownership, svc: svc, plan: plan, period: period, sub: sub}
}

func (f *fixture) append(t *testing.T, key string, meter commercev1.Meter, kind commercev1.UsageKind, quantity int64, resource string) {
	t.Helper()
	if resource == "" {
		resource = "app-1"
	}
	err := f.svc.Append(f.ctx, commercev1.UsageEvent{TenantID: "tenant-1", PeriodID: f.period.ID, ResourceType: "application", ResourceID: resource, Meter: meter, Kind: kind, Quantity: quantity, IdempotencyKey: key, OccurredAt: baseTime.Add(time.Hour), WindowStart: baseTime, WindowEnd: baseTime.Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
}
func expectCode(t *testing.T, err error, code domain.Code) {
	t.Helper()
	if !domain.HasCode(err, code) {
		t.Fatalf("error=%v want code=%s", err, code)
	}
}
func usageQuantity(events []commercev1.UsageEvent, meter commercev1.Meter) int64 {
	var q int64
	for _, e := range events {
		if e.Meter == meter {
			q += e.Quantity
		}
	}
	return q
}

func TestPlan_ActiveVersionIsImmutable(t *testing.T) {
	f := newFixture(t)
	spec := planSpec()
	spec.Quotas["runtime.units"] = 99
	_, err := f.svc.ReplacePlanSpec(f.ctx, f.plan.ID, spec)
	expectCode(t, err, domain.CodeConflict)
}
func TestPlan_NewVersionDoesNotChangeExistingPeriod(t *testing.T) {
	f := newFixture(t)
	spec := planSpec()
	spec.Prices[commercev1.MeterRuntimeUnitSeconds] = commercev1.Price{MinorUnits: 9, PerQuantity: 3600}
	v2, err := f.svc.CreatePlanVersion(f.ctx, application.CreatePlanVersionCommand{ID: "plan-v2", DefinitionID: "plan", PolicyVersion: "policy-v2", Number: 2, Spec: spec, EffectiveFrom: baseTime.Add(24 * time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.svc.ActivatePlanVersion(f.ctx, v2.ID); err != nil {
		t.Fatal(err)
	}
	preview, err := f.svc.PreviewInvoice(f.ctx, "tenant-1", f.period.ID)
	if err != nil {
		t.Fatal(err)
	}
	if preview.PlanVersionID != f.plan.ID || preview.PolicyVersion != "policy-v1" {
		t.Fatalf("preview=%+v", preview)
	}
}
func TestPlan_DisabledVersionRejectsNewSubscription(t *testing.T) {
	f := newFixture(t)
	spec := planSpec()
	v2, err := f.svc.CreatePlanVersion(f.ctx, application.CreatePlanVersionCommand{ID: "disabled", DefinitionID: "plan", PolicyVersion: "disabled-policy", Number: 2, Spec: spec, EffectiveFrom: baseTime})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.svc.DisablePlanVersion(f.ctx, v2.ID); err != nil {
		t.Fatal(err)
	}
	_, _, err = f.svc.StartSubscription(f.ctx, application.StartSubscriptionCommand{ID: "sub-x", TenantID: "tenant-x", PlanVersionID: v2.ID, PeriodID: "p-x", State: domain.SubscriptionActive, PeriodStart: baseTime, PeriodEnd: baseTime.Add(time.Hour)})
	expectCode(t, err, domain.CodeConflict)
}
func TestPlan_MeterReferencesMustExist(t *testing.T) {
	f := newFixture(t)
	spec := planSpec()
	spec.Prices[commercev1.Meter("made.up")] = commercev1.Price{MinorUnits: 1, PerQuantity: 1}
	_, err := f.svc.CreatePlanVersion(f.ctx, application.CreatePlanVersionCommand{ID: "bad", DefinitionID: "plan", PolicyVersion: "bad", Number: 7, Spec: spec, EffectiveFrom: baseTime})
	expectCode(t, err, domain.CodeInvalidArgument)
}
func TestPlan_PricesUseIntegerMinorUnits(t *testing.T) {
	raw, err := json.Marshal(planSpec().Prices[commercev1.MeterRuntimeUnitSeconds])
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "{\"minor_units\":1,\"per_quantity\":3600}" {
		t.Fatalf("json=%s", raw)
	}
}

func TestEntitlement_AllowsFeatureIncludedInPlan(t *testing.T) {
	f := newFixture(t)
	d, err := f.svc.Check(f.ctx, commercev1.EntitlementRequest{TenantID: "tenant-1", Feature: "deploy", At: f.clock.Now()})
	if err != nil || !d.Allowed {
		t.Fatalf("decision=%+v err=%v", d, err)
	}
}
func TestEntitlement_DeniesMissingFeature(t *testing.T) {
	f := newFixture(t)
	d, err := f.svc.Check(f.ctx, commercev1.EntitlementRequest{TenantID: "tenant-1", Feature: "gpu", At: f.clock.Now()})
	if err != nil || d.Allowed {
		t.Fatalf("decision=%+v err=%v", d, err)
	}
}
func TestEntitlement_DeniesSuspendedAccount(t *testing.T) {
	f := newFixture(t)
	if _, err := f.svc.Suspend(f.ctx, "tenant-1"); err != nil {
		t.Fatal(err)
	}
	d, err := f.svc.Check(f.ctx, commercev1.EntitlementRequest{TenantID: "tenant-1", Feature: "deploy", At: f.clock.Now()})
	if err != nil || d.Allowed {
		t.Fatalf("decision=%+v err=%v", d, err)
	}
}
func TestEntitlement_TrialHasExplicitLimits(t *testing.T) {
	f := newFixture(t)
	d, err := f.svc.Check(f.ctx, commercev1.EntitlementRequest{TenantID: "tenant-1", Feature: "deploy", Resource: "runtime.units", Quantity: 2, At: f.clock.Now()})
	if err != nil || !d.Allowed || d.Limit != 2 {
		t.Fatalf("decision=%+v err=%v", d, err)
	}
}
func TestEntitlement_DecisionContainsReasonAndPolicyVersion(t *testing.T) {
	f := newFixture(t)
	d, err := f.svc.Check(f.ctx, commercev1.EntitlementRequest{TenantID: "tenant-1", Feature: "gpu", At: f.clock.Now()})
	if err != nil || d.Reason == "" || d.PolicyVersion != "policy-v1" {
		t.Fatalf("decision=%+v err=%v", d, err)
	}
}
func TestEntitlement_CrossTenantLookupDenied(t *testing.T) {
	f := newFixture(t)
	_, err := f.svc.CheckAs(f.ctx, "tenant-2", commercev1.EntitlementRequest{TenantID: "tenant-1", Feature: "deploy", At: f.clock.Now()})
	expectCode(t, err, domain.CodePermissionDenied)
}

func TestQuota_ReserveWithinLimit(t *testing.T) {
	f := newFixture(t)
	q, err := f.svc.Reserve(f.ctx, commercev1.QuotaRequest{TenantID: "tenant-1", Resource: "runtime.units", Quantity: 1, IdempotencyKey: "q1", At: f.clock.Now(), ExpiresAt: f.clock.Now().Add(time.Hour)})
	if err != nil || q.State != "RESERVED" {
		t.Fatalf("q=%+v err=%v", q, err)
	}
}
func TestQuota_RejectAboveLimit(t *testing.T) {
	f := newFixture(t)
	_, err := f.svc.Reserve(f.ctx, commercev1.QuotaRequest{TenantID: "tenant-1", Resource: "runtime.units", Quantity: 3, IdempotencyKey: "q1", At: f.clock.Now(), ExpiresAt: f.clock.Now().Add(time.Hour)})
	expectCode(t, err, domain.CodeQuotaExceeded)
}
func TestQuota_ConcurrentReservationsCannotOversubscribe(t *testing.T) {
	f := newFixture(t)
	const n = 24
	var wg sync.WaitGroup
	start := make(chan struct{})
	success := 0
	var mu sync.Mutex
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, err := f.svc.Reserve(f.ctx, commercev1.QuotaRequest{TenantID: "tenant-1", Resource: "runtime.units", Quantity: 1, IdempotencyKey: fmt.Sprintf("q-%d", i), At: f.clock.Now(), ExpiresAt: f.clock.Now().Add(time.Hour)})
			if err == nil {
				mu.Lock()
				success++
				mu.Unlock()
			} else if !domain.HasCode(err, domain.CodeQuotaExceeded) {
				t.Errorf("unexpected err %v", err)
			}
		}(i)
	}
	close(start)
	wg.Wait()
	if success != 2 {
		t.Fatalf("success=%d", success)
	}
}
func TestQuota_ReleaseMakesCapacityAvailable(t *testing.T) {
	f := newFixture(t)
	q, err := f.svc.Reserve(f.ctx, commercev1.QuotaRequest{TenantID: "tenant-1", Resource: "runtime.units", Quantity: 2, IdempotencyKey: "full", At: f.clock.Now(), ExpiresAt: f.clock.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if err = f.svc.Release(f.ctx, q.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.svc.Reserve(f.ctx, commercev1.QuotaRequest{TenantID: "tenant-1", Resource: "runtime.units", Quantity: 2, IdempotencyKey: "again", At: f.clock.Now(), ExpiresAt: f.clock.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
}
func TestQuota_CommitIsIdempotent(t *testing.T) {
	f := newFixture(t)
	q, err := f.svc.Reserve(f.ctx, commercev1.QuotaRequest{TenantID: "tenant-1", Resource: "runtime.units", Quantity: 1, IdempotencyKey: "q", At: f.clock.Now(), ExpiresAt: f.clock.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if err = f.svc.Commit(f.ctx, q.ID); err != nil {
		t.Fatal(err)
	}
	if err = f.svc.Commit(f.ctx, q.ID); err != nil {
		t.Fatal(err)
	}
}
func TestQuota_ExpiredReservationIsReclaimed(t *testing.T) {
	f := newFixture(t)
	_, err := f.svc.Reserve(f.ctx, commercev1.QuotaRequest{TenantID: "tenant-1", Resource: "runtime.units", Quantity: 2, IdempotencyKey: "old", At: f.clock.Now(), ExpiresAt: f.clock.Now().Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	f.clock.Advance(2 * time.Minute)
	_, err = f.svc.Reserve(f.ctx, commercev1.QuotaRequest{TenantID: "tenant-1", Resource: "runtime.units", Quantity: 2, IdempotencyKey: "new", At: f.clock.Now(), ExpiresAt: f.clock.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
}

func TestUsage_AppendValidEvent(t *testing.T) {
	f := newFixture(t)
	f.append(t, "u1", commercev1.MeterRuntimeUnitSeconds, commercev1.UsageStandard, 60, "")
	if q := usageQuantity(f.store.Usage("tenant-1", f.period.ID), commercev1.MeterRuntimeUnitSeconds); q != 60 {
		t.Fatalf("q=%d", q)
	}
}
func TestUsage_DuplicateIdempotencyKeyIgnored(t *testing.T) {
	f := newFixture(t)
	f.append(t, "u1", commercev1.MeterRuntimeUnitSeconds, commercev1.UsageStandard, 60, "")
	f.append(t, "u1", commercev1.MeterRuntimeUnitSeconds, commercev1.UsageStandard, 60, "")
	if len(f.store.Usage("tenant-1", f.period.ID)) != 1 {
		t.Fatal("duplicate charged")
	}
}
func TestUsage_SameKeyDifferentQuantityRejected(t *testing.T) {
	f := newFixture(t)
	f.append(t, "u1", commercev1.MeterRuntimeUnitSeconds, commercev1.UsageStandard, 60, "")
	err := f.svc.Append(f.ctx, commercev1.UsageEvent{TenantID: "tenant-1", PeriodID: f.period.ID, ResourceType: "application", ResourceID: "app-1", Meter: commercev1.MeterRuntimeUnitSeconds, Kind: commercev1.UsageStandard, Quantity: 61, IdempotencyKey: "u1", OccurredAt: baseTime.Add(time.Hour), WindowStart: baseTime, WindowEnd: baseTime.Add(time.Hour)})
	expectCode(t, err, domain.CodeConflict)
}
func TestUsage_NegativeQuantityRejectedExceptCorrectionType(t *testing.T) {
	f := newFixture(t)
	err := f.svc.Append(f.ctx, commercev1.UsageEvent{TenantID: "tenant-1", PeriodID: f.period.ID, ResourceType: "application", ResourceID: "app-1", Meter: commercev1.MeterRuntimeUnitSeconds, Kind: commercev1.UsageStandard, Quantity: -1, IdempotencyKey: "neg", OccurredAt: baseTime.Add(time.Hour), WindowStart: baseTime, WindowEnd: baseTime.Add(time.Hour)})
	expectCode(t, err, domain.CodeInvalidArgument)
	f.append(t, "corr", commercev1.MeterRuntimeUnitSeconds, commercev1.UsageCorrection, -1, "")
}
func TestUsage_EventOutsideTenantResourceRejected(t *testing.T) {
	f := newFixture(t)
	err := f.svc.Append(f.ctx, commercev1.UsageEvent{TenantID: "tenant-1", PeriodID: f.period.ID, ResourceType: "application", ResourceID: "app-foreign", Meter: commercev1.MeterRuntimeUnitSeconds, Kind: commercev1.UsageStandard, Quantity: 1, IdempotencyKey: "foreign", OccurredAt: baseTime.Add(time.Hour), WindowStart: baseTime, WindowEnd: baseTime.Add(time.Hour)})
	expectCode(t, err, domain.CodePermissionDenied)
}
func TestUsage_LateEventRoutesToCorrectOpenPeriod(t *testing.T) {
	f := newFixture(t)
	f.clock.Set(baseTime.Add(40 * 24 * time.Hour))
	err := f.svc.Append(f.ctx, commercev1.UsageEvent{TenantID: "tenant-1", ResourceType: "application", ResourceID: "app-1", Meter: commercev1.MeterRuntimeUnitSeconds, Kind: commercev1.UsageStandard, Quantity: 60, IdempotencyKey: "late", OccurredAt: baseTime.Add(5 * 24 * time.Hour), WindowStart: baseTime.Add(5 * 24 * time.Hour), WindowEnd: baseTime.Add(5*24*time.Hour + time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	events := f.store.Usage("tenant-1", f.period.ID)
	if len(events) != 1 || events[0].PeriodID != f.period.ID {
		t.Fatalf("events=%+v", events)
	}
}

func TestRuntimeUsage_OneUnitForOneHourEquals3600UnitSeconds(t *testing.T) {
	q := application.CalculateRuntimeUnitSeconds([]commercev1.RuntimeObservation{{At: baseTime, Replicas: 1, UnitWeight: 1}}, baseTime, baseTime.Add(time.Hour))
	if q != 3600 {
		t.Fatalf("q=%d", q)
	}
}
func TestRuntimeUsage_ReplicaScaleProducesPiecewiseUsage(t *testing.T) {
	q := application.CalculateRuntimeUnitSeconds([]commercev1.RuntimeObservation{{At: baseTime, Replicas: 1, UnitWeight: 1}, {At: baseTime.Add(30 * time.Minute), Replicas: 3, UnitWeight: 1}}, baseTime, baseTime.Add(time.Hour))
	if q != 7200 {
		t.Fatalf("q=%d", q)
	}
}
func TestRuntimeUsage_SuspensionStopsComputeUsageAtBoundary(t *testing.T) {
	q := application.CalculateRuntimeUnitSeconds([]commercev1.RuntimeObservation{{At: baseTime, Replicas: 1, UnitWeight: 1}, {At: baseTime.Add(30 * time.Minute), Replicas: 1, UnitWeight: 1, Suspended: true}}, baseTime, baseTime.Add(time.Hour))
	if q != 1800 {
		t.Fatalf("q=%d", q)
	}
}
func TestRuntimeUsage_ClockSkewDoesNotCreateNegativeInterval(t *testing.T) {
	q := application.CalculateRuntimeUnitSeconds([]commercev1.RuntimeObservation{{At: baseTime.Add(time.Hour), Replicas: 1, UnitWeight: 1}, {At: baseTime, Replicas: 1, UnitWeight: 1}}, baseTime.Add(2*time.Hour), baseTime)
	if q != 0 {
		t.Fatalf("q=%d", q)
	}
}
func TestRuntimeUsage_OverlappingObservationsAreDeduplicated(t *testing.T) {
	q := application.CalculateRuntimeUnitSeconds([]commercev1.RuntimeObservation{{At: baseTime, Replicas: 1, UnitWeight: 1}, {At: baseTime, Replicas: 2, UnitWeight: 1}}, baseTime, baseTime.Add(time.Hour))
	if q != 7200 {
		t.Fatalf("q=%d", q)
	}
}

func TestBuildRating_SuccessIsCharged(t *testing.T) {
	f := newFixture(t)
	in := buildUsage(commercev1.BuildSucceeded)
	if err := f.svc.RecordBuildUsage(f.ctx, in); err != nil {
		t.Fatal(err)
	}
	if usageQuantity(f.store.Usage("tenant-1", f.period.ID), commercev1.MeterBuildCPUSeconds) != 120 {
		t.Fatal("cpu not charged")
	}
}
func TestBuildRating_UserFailureIsChargedByPolicy(t *testing.T) {
	f := newFixture(t)
	if err := f.svc.RecordBuildUsage(f.ctx, buildUsage(commercev1.BuildUserFailed)); err != nil {
		t.Fatal(err)
	}
	if len(f.store.Usage("tenant-1", f.period.ID)) == 0 {
		t.Fatal("user failure not charged")
	}
}
func TestBuildRating_PlatformFailureIsNotCharged(t *testing.T) {
	f := newFixture(t)
	if err := f.svc.RecordBuildUsage(f.ctx, buildUsage(commercev1.BuildPlatformFailed)); err != nil {
		t.Fatal(err)
	}
	if len(f.store.Usage("tenant-1", f.period.ID)) != 0 {
		t.Fatal("platform failure charged")
	}
}
func TestBuildRating_CanceledBeforeStartIsNotCharged(t *testing.T) {
	f := newFixture(t)
	in := buildUsage(commercev1.BuildCanceled)
	in.FinishedAt = in.StartedAt
	if err := f.svc.RecordBuildUsage(f.ctx, in); err != nil {
		t.Fatal(err)
	}
	if len(f.store.Usage("tenant-1", f.period.ID)) != 0 {
		t.Fatal("canceled build charged")
	}
}
func TestBuildRating_DockerVMUsesSeparateMeter(t *testing.T) {
	f := newFixture(t)
	if err := f.svc.RecordBuildUsage(f.ctx, buildUsage(commercev1.BuildSucceeded)); err != nil {
		t.Fatal(err)
	}
	if usageQuantity(f.store.Usage("tenant-1", f.period.ID), commercev1.MeterBuildDockerVMSeconds) != 60 {
		t.Fatal("docker vm meter missing")
	}
}
func buildUsage(outcome commercev1.BuildOutcome) commercev1.BuildUsage {
	return commercev1.BuildUsage{TenantID: "tenant-1", BuildID: "build-1", Outcome: outcome, StartedAt: baseTime.Add(time.Minute), FinishedAt: baseTime.Add(11 * time.Minute), CPUSeconds: 120, MemoryGiBSeconds: 240, DockerVMSeconds: 60, IdempotencyKey: "build-usage"}
}

func TestRating_UsesUTCPeriodBoundaries(t *testing.T) {
	p, err := domain.NewBillingPeriod("p", "t", "s", "v", time.Date(2026, 1, 1, 0, 0, 0, 0, time.FixedZone("x", 2*3600)), time.Date(2026, 2, 1, 0, 0, 0, 0, time.FixedZone("x", 2*3600)), baseTime)
	if err != nil {
		t.Fatal(err)
	}
	if !p.Contains(time.Date(2025, 12, 31, 22, 0, 0, 0, time.UTC)) {
		t.Fatal("UTC boundary mismatch")
	}
}
func TestRating_DaylightSavingHasNoEffect(t *testing.T) {
	loc, err := time.LoadLocation("Europe/Zurich")
	if err != nil {
		t.Skip(err)
	}
	start := time.Date(2026, 3, 29, 1, 30, 0, 0, loc)
	end := start.Add(time.Hour)
	q := application.CalculateRuntimeUnitSeconds([]commercev1.RuntimeObservation{{At: start, Replicas: 1, UnitWeight: 1}}, start, end)
	if q != 3600 {
		t.Fatalf("q=%d start=%s end=%s", q, start, end)
	}
}
func TestRating_RoundsOnlyAtDefinedStage(t *testing.T) {
	a, err := application.RateQuantity(1, commercev1.Price{MinorUnits: 1, PerQuantity: 3})
	if err != nil || a != 0 {
		t.Fatalf("a=%d err=%v", a, err)
	}
	b, err := application.RateQuantity(2, commercev1.Price{MinorUnits: 1, PerQuantity: 3})
	if err != nil || b != 1 {
		t.Fatalf("b=%d err=%v", b, err)
	}
}
func TestRating_MicroUsageAccumulatesBeforeRounding(t *testing.T) {
	f := newFixture(t)
	spec := planSpec()
	spec.Prices[commercev1.MeterRuntimeUnitSeconds] = commercev1.Price{MinorUnits: 1, PerQuantity: 3}
	var pv domain.PlanVersion
	_ = f.store.Transact(f.ctx, func(tx application.Tx) error {
		v, _ := tx.GetPlanVersion(f.plan.ID)
		old := v.Version
		v.State = domain.PlanDraft
		if err := tx.UpdatePlanVersion(v, old); err != nil {
			return err
		}
		pv = v
		return nil
	})
	_ = pv // immutable active plans are intentionally not modified; use three seconds at default price instead.
	f.append(t, "m1", commercev1.MeterRuntimeUnitSeconds, commercev1.UsageStandard, 1800, "")
	f.append(t, "m2", commercev1.MeterRuntimeUnitSeconds, commercev1.UsageStandard, 1800, "")
	preview, err := f.svc.PreviewInvoice(f.ctx, "tenant-1", f.period.ID)
	if err != nil {
		t.Fatal(err)
	}
	if preview.TotalMinorUnits != 1 {
		t.Fatalf("preview=%+v", preview)
	}
}
func TestRating_LargeQuantityDoesNotOverflow(t *testing.T) {
	amount, err := application.RateQuantity(math.MaxInt64/2, commercev1.Price{MinorUnits: 1, PerQuantity: 1})
	if err != nil || amount != math.MaxInt64/2 {
		t.Fatalf("amount=%d err=%v", amount, err)
	}
}

func TestRating_UsesExactArithmeticAcrossMicroUsageAndLargeQuantities(t *testing.T) {
	testCases := [][3]int64{
		{0, 1, 3}, {1, 1, 3}, {2, 1, 3}, {-2, 1, 3},
		{math.MaxInt64, 1, math.MaxInt64},
		{math.MinInt64, 1, math.MaxInt64},
		{math.MaxInt64, math.MaxInt64, 1},
	}
	random := rand.New(rand.NewSource(20260714))
	for range 5000 {
		quantity := random.Int63()
		if random.Intn(2) == 0 {
			quantity = -quantity
		}
		testCases = append(testCases, [3]int64{quantity, random.Int63(), random.Int63n(math.MaxInt64) + 1})
	}
	for _, input := range testCases {
		assertExactRating(t, input[0], input[1], input[2])
	}
}

func assertExactRating(t testing.TB, quantity, minor, per int64) {
	t.Helper()
	want, fits := rationalRatingReference(quantity, minor, per)
	got, err := application.RateQuantity(quantity, commercev1.Price{MinorUnits: minor, PerQuantity: per})
	if !fits {
		if !domain.HasCode(err, domain.CodeOverflow) {
			t.Fatalf("quantity=%d minor=%d per=%d: got=%d err=%v, want overflow", quantity, minor, per, got, err)
		}
		return
	}
	if err != nil || got != want {
		t.Fatalf("quantity=%d minor=%d per=%d: got=%d err=%v, want=%d", quantity, minor, per, got, err, want)
	}
}

func rationalRatingReference(quantity, minor, per int64) (int64, bool) {
	value := new(big.Rat).SetFrac(
		new(big.Int).Mul(big.NewInt(quantity), big.NewInt(minor)),
		big.NewInt(per),
	)
	numerator := new(big.Int).Abs(new(big.Int).Set(value.Num()))
	denominator := new(big.Int).Set(value.Denom())
	quotient, remainder := new(big.Int), new(big.Int)
	quotient.QuoRem(numerator, denominator, remainder)
	if new(big.Int).Lsh(remainder, 1).Cmp(denominator) >= 0 {
		quotient.Add(quotient, big.NewInt(1))
	}
	if value.Sign() < 0 {
		quotient.Neg(quotient)
	}
	return quotient.Int64(), quotient.IsInt64()
}

func TestInvoicePreview_IsDeterministic(t *testing.T) {
	f := newFixture(t)
	f.append(t, "u", commercev1.MeterRuntimeUnitSeconds, commercev1.UsageStandard, 3600, "")
	a, err := f.svc.PreviewInvoice(f.ctx, "tenant-1", f.period.ID)
	if err != nil {
		t.Fatal(err)
	}
	b, err := f.svc.PreviewInvoice(f.ctx, "tenant-1", f.period.ID)
	if err != nil {
		t.Fatal(err)
	}
	ra, _ := json.Marshal(a)
	rb, _ := json.Marshal(b)
	if string(ra) != string(rb) {
		t.Fatalf("a=%s b=%s", ra, rb)
	}
}
func TestInvoicePreview_GroupsByMeterAndResource(t *testing.T) {
	f := newFixture(t)
	f.append(t, "a", commercev1.MeterRuntimeUnitSeconds, commercev1.UsageStandard, 1800, "")
	f.append(t, "b", commercev1.MeterRuntimeUnitSeconds, commercev1.UsageStandard, 1800, "")
	p, err := f.svc.PreviewInvoice(f.ctx, "tenant-1", f.period.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Lines) != 1 || p.Lines[0].Quantity != 3600 {
		t.Fatalf("p=%+v", p)
	}
}
func TestInvoicePreview_AppliesIncludedAllowanceBeforeOverage(t *testing.T) {
	f := newFixture(t)
	if err := f.store.Transact(f.ctx, func(tx application.Tx) error {
		v, _ := tx.GetPlanVersion(f.plan.ID)
		period, _ := tx.GetBillingPeriod(f.period.ID)
		copy := v
		copy.ID = "allowance"
		copy.PolicyVersion = "allowance"
		copy.Number = 2
		copy.State = domain.PlanDraft
		copy.Spec = planSpec()
		copy.Spec.Included[commercev1.MeterRuntimeUnitSeconds] = 3600
		copy.Version = 1
		copy.CreatedAt = f.clock.Now()
		copy.UpdatedAt = f.clock.Now()
		if err := tx.InsertPlanVersion(copy); err != nil {
			return err
		}
		old := copy.Version
		if err := copy.Activate(f.clock.Now()); err != nil {
			return err
		}
		if err := tx.UpdatePlanVersion(copy, old); err != nil {
			return err
		}
		po := period.Version
		period.PlanVersionID = copy.ID
		period.Version++
		return tx.UpdateBillingPeriod(period, po)
	}); err != nil {
		t.Fatal(err)
	}
	f.append(t, "u", commercev1.MeterRuntimeUnitSeconds, commercev1.UsageStandard, 7200, "")
	p, err := f.svc.PreviewInvoice(f.ctx, "tenant-1", f.period.ID)
	if err != nil {
		t.Fatal(err)
	}
	if p.Lines[0].BillableQuantity != 3600 || p.TotalMinorUnits != 1 {
		t.Fatalf("p=%+v", p)
	}
}
func TestInvoicePreview_CreditsAreSeparateLedgerEntries(t *testing.T) {
	f := newFixture(t)
	f.append(t, "u", commercev1.MeterRuntimeUnitSeconds, commercev1.UsageStandard, 3600, "")
	f.append(t, "c", commercev1.MeterRuntimeUnitSeconds, commercev1.UsageCredit, -3600, "")
	p, err := f.svc.PreviewInvoice(f.ctx, "tenant-1", f.period.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Lines) != 2 || p.TotalMinorUnits != 0 {
		t.Fatalf("p=%+v", p)
	}
}
func TestInvoicePreview_PriceVersionMatchesUsageTimestamp(t *testing.T) {
	f := newFixture(t)
	f.append(t, "u", commercev1.MeterRuntimeUnitSeconds, commercev1.UsageStandard, 3600, "")
	p, err := f.svc.PreviewInvoice(f.ctx, "tenant-1", f.period.ID)
	if err != nil {
		t.Fatal(err)
	}
	if p.PlanVersionID != "plan-v1" || p.PolicyVersion != "policy-v1" {
		t.Fatalf("p=%+v", p)
	}
}

func TestCommercialState_GracePeriodDoesNotDeleteRuntime(t *testing.T) {
	f := newFixture(t)
	intent, err := f.svc.SetGrace(f.ctx, "tenant-1")
	if err != nil || intent.Action != "retain" || !intent.RetainManagedServices {
		t.Fatalf("intent=%+v err=%v", intent, err)
	}
	d, err := f.svc.Check(f.ctx, commercev1.EntitlementRequest{TenantID: "tenant-1", Feature: "deploy", At: f.clock.Now()})
	if err != nil || !d.Allowed {
		t.Fatalf("existing feature denied in grace: %+v %v", d, err)
	}
}
func TestCommercialState_SuspensionEmitsSuspendIntent(t *testing.T) {
	f := newFixture(t)
	intent, err := f.svc.Suspend(f.ctx, "tenant-1")
	if err != nil || intent.Action != "suspend" {
		t.Fatalf("intent=%+v err=%v", intent, err)
	}
	if len(f.store.Outbox()) < 2 {
		t.Fatal("suspend outbox missing")
	}
}
func TestCommercialState_SuspensionRetainsManagedServices(t *testing.T) {
	f := newFixture(t)
	intent, err := f.svc.Suspend(f.ctx, "tenant-1")
	if err != nil || !intent.RetainManagedServices {
		t.Fatalf("intent=%+v err=%v", intent, err)
	}
}
func TestCommercialState_ResumptionEmitsResumeIntent(t *testing.T) {
	f := newFixture(t)
	_, _ = f.svc.Suspend(f.ctx, "tenant-1")
	intent, err := f.svc.Resume(f.ctx, "tenant-1")
	if err != nil || intent.Action != "resume" {
		t.Fatalf("intent=%+v err=%v", intent, err)
	}
}
func TestCommercialState_AlreadySuspendedIsIdempotent(t *testing.T) {
	f := newFixture(t)
	_, err := f.svc.Suspend(f.ctx, "tenant-1")
	if err != nil {
		t.Fatal(err)
	}
	before := len(f.store.Outbox())
	_, err = f.svc.Suspend(f.ctx, "tenant-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(f.store.Outbox()) != before {
		t.Fatal("duplicate suspension intent")
	}
}

func TestUsageReconciler_ObservedAllocationMatchesLedger(t *testing.T) {
	f := newFixture(t)
	f.append(t, "u", commercev1.MeterRuntimeUnitSeconds, commercev1.UsageStandard, 3600, "")
	q, err := f.svc.ReconcileUsage(f.ctx, reconcileCommand(3600))
	if err != nil || q != 0 {
		t.Fatalf("q=%d err=%v", q, err)
	}
}
func TestUsageReconciler_MissingIntervalCreatesCorrection(t *testing.T) {
	f := newFixture(t)
	q, err := f.svc.ReconcileUsage(f.ctx, reconcileCommand(3600))
	if err != nil || q != 3600 {
		t.Fatalf("q=%d err=%v", q, err)
	}
	events := f.store.Usage("tenant-1", f.period.ID)
	if len(events) != 1 || events[0].Kind != commercev1.UsageCorrection {
		t.Fatalf("events=%+v", events)
	}
}
func TestUsageReconciler_DuplicateCorrectionIsPrevented(t *testing.T) {
	f := newFixture(t)
	if _, err := f.svc.ReconcileUsage(f.ctx, reconcileCommand(3600)); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.ReconcileUsage(f.ctx, reconcileCommand(3600)); err != nil {
		t.Fatal(err)
	}
	if len(f.store.Usage("tenant-1", f.period.ID)) != 1 {
		t.Fatal("duplicate correction")
	}
}
func TestUsageReconciler_DriftAboveThresholdRaisesAlert(t *testing.T) {
	f := newFixture(t)
	f.svc.DriftAlertThreshold = 100
	if _, err := f.svc.ReconcileUsage(f.ctx, reconcileCommand(3600)); err != nil {
		t.Fatal(err)
	}
	if len(f.store.Alerts("tenant-1", f.period.ID)) != 1 {
		t.Fatal("alert missing")
	}
}
func reconcileCommand(q int64) application.ReconcileUsageCommand {
	return application.ReconcileUsageCommand{TenantID: "tenant-1", PeriodID: "period-1", ResourceType: "application", ResourceID: "app-1", Meter: commercev1.MeterRuntimeUnitSeconds, WindowStart: baseTime, WindowEnd: baseTime.Add(time.Hour), ObservedQuantity: q}
}

func TestUsage_MissingOwnershipResolverFailsClosed(t *testing.T) {
	f := newFixture(t)
	f.svc.Ownership = nil
	err := f.svc.Append(f.ctx, commercev1.UsageEvent{TenantID: "tenant-1", PeriodID: f.period.ID, ResourceType: "application", ResourceID: "app-1", Meter: commercev1.MeterRuntimeUnitSeconds, Quantity: 1, IdempotencyKey: "closed", OccurredAt: baseTime.Add(time.Hour), WindowStart: baseTime, WindowEnd: baseTime.Add(time.Hour)})
	expectCode(t, err, domain.CodePermissionDenied)
}
func TestEntitlement_ExpiredTrialDeniesAllocation(t *testing.T) {
	f := newFixture(t)
	_ = f.store.Transact(f.ctx, func(tx application.Tx) error {
		sub, _ := tx.GetSubscription(f.sub.ID)
		old := sub.Version
		sub.State = domain.SubscriptionTrial
		sub.TrialEndsAt = f.clock.Now().Add(-time.Second)
		sub.Version++
		return tx.UpdateSubscription(sub, old)
	})
	_, err := f.svc.Reserve(f.ctx, commercev1.QuotaRequest{TenantID: "tenant-1", Resource: "runtime.units", Quantity: 1, IdempotencyKey: "expired", At: f.clock.Now(), ExpiresAt: f.clock.Now().Add(time.Hour)})
	expectCode(t, err, domain.CodePermissionDenied)
}
func TestQuota_CommitAfterExpiryIsRejected(t *testing.T) {
	f := newFixture(t)
	q, err := f.svc.Reserve(f.ctx, commercev1.QuotaRequest{TenantID: "tenant-1", Resource: "runtime.units", Quantity: 1, IdempotencyKey: "exp", At: f.clock.Now(), ExpiresAt: f.clock.Now().Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	f.clock.Advance(2 * time.Minute)
	err = f.svc.Commit(f.ctx, q.ID)
	expectCode(t, err, domain.CodeConflict)
}
func TestUsage_WindowOutsidePeriodRejected(t *testing.T) {
	f := newFixture(t)
	err := f.svc.Append(f.ctx, commercev1.UsageEvent{TenantID: "tenant-1", PeriodID: f.period.ID, ResourceType: "application", ResourceID: "app-1", Meter: commercev1.MeterRuntimeUnitSeconds, Quantity: 1, IdempotencyKey: "outside", OccurredAt: baseTime.Add(time.Hour), WindowStart: baseTime.Add(-time.Hour), WindowEnd: baseTime.Add(time.Hour)})
	expectCode(t, err, domain.CodeInvalidArgument)
}
func TestCommercialAccount_InvalidBackwardTransitionRejected(t *testing.T) {
	f := newFixture(t)
	_, _ = f.svc.Suspend(f.ctx, "tenant-1")
	_, err := f.svc.SetGrace(f.ctx, "tenant-1")
	expectCode(t, err, domain.CodeConflict)
}
func TestBuildUsage_AllMetersAreAtomic(t *testing.T) {
	f := newFixture(t)
	f.ownership.Err = errors.New("ownership unavailable")
	err := f.svc.RecordBuildUsage(f.ctx, buildUsage(commercev1.BuildSucceeded))
	expectCode(t, err, domain.CodeUnavailable)
	if len(f.store.Usage("tenant-1", f.period.ID)) != 0 {
		t.Fatal("partial build usage")
	}
}

func FuzzRuntimeUsageNoPanic(f *testing.F) {
	f.Add(int64(1), int64(1), int64(3600))
	f.Fuzz(func(t *testing.T, replicas, weight, seconds int64) {
		if replicas < 0 {
			replicas = -replicas
		}
		if weight < 0 {
			weight = -weight
		}
		if seconds < 0 {
			seconds = -seconds
		}
		replicas %= 1000
		weight %= 1000
		seconds %= 86400
		start := baseTime
		_ = application.CalculateRuntimeUnitSeconds([]commercev1.RuntimeObservation{{At: start, Replicas: replicas, UnitWeight: weight}}, start, start.Add(time.Duration(seconds)*time.Second))
	})
}
func FuzzRateQuantityNoPanic(f *testing.F) {
	f.Add(int64(1), int64(1), int64(1))
	f.Fuzz(func(t *testing.T, q, minor, per int64) {
		if minor < 0 {
			minor = -minor
		}
		if per < 0 {
			per = -per
		}
		if per == 0 {
			per = 1
		}
		_, _ = application.RateQuantity(q, commercev1.Price{MinorUnits: minor, PerQuantity: per})
	})
}

func FuzzRatingExactArithmeticMatchesRationalReference(f *testing.F) {
	f.Add(int64(1), uint64(1), uint64(3))
	f.Add(int64(math.MaxInt64), uint64(math.MaxInt64), uint64(1))
	f.Fuzz(func(t *testing.T, quantity int64, minorRaw, perRaw uint64) {
		minor := int64(minorRaw & uint64(math.MaxInt64))
		per := int64(perRaw%uint64(math.MaxInt64)) + 1
		assertExactRating(t, quantity, minor, per)
	})
}
