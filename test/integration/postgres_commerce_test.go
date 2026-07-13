//go:build postgres_integration

package integration_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/keir-research/ai-native-paas/internal/commerce/application"
	"github.com/keir-research/ai-native-paas/internal/commerce/domain"
	commercepostgres "github.com/keir-research/ai-native-paas/internal/commerce/postgres"
	"github.com/keir-research/ai-native-paas/internal/commerce/testkit"
	commercev1 "github.com/keir-research/ai-native-paas/pkg/contracts/commerce/v1"
)

var commerceBaseTime = time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)

func migratedCommerceStore(t *testing.T) (*sql.DB, *commercepostgres.Store) {
	t.Helper()
	db := openPostgres(t)
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `DROP SCHEMA IF EXISTS commerce CASCADE`); err != nil {
		t.Fatalf("reset commerce schema: %v", err)
	}
	store, err := commercepostgres.NewStore(db)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("migrate commerce: %v", err)
	}
	return db, store
}

func commercePlanSpec() commercev1.PlanSpec {
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

type postgresCommerceFixture struct {
	db     *sql.DB
	store  *commercepostgres.Store
	clock  *testkit.Clock
	ids    *testkit.IDs
	svc    *application.Service
	plan   domain.PlanVersion
	period domain.BillingPeriod
}

func newPostgresCommerceFixture(t *testing.T) *postgresCommerceFixture {
	t.Helper()
	db, store := migratedCommerceStore(t)
	clock := &testkit.Clock{T: commerceBaseTime.Add(time.Hour)}
	ids := &testkit.IDs{}
	svc := &application.Service{Store: store, Clock: clock, IDs: ids, Ownership: application.AllowAllOwnership{}, DriftAlertThreshold: 30}
	ctx := context.Background()
	if _, err := svc.CreatePlanDefinition(ctx, application.CreatePlanDefinitionCommand{ID: "plan", Name: "Developer"}); err != nil {
		t.Fatal(err)
	}
	plan, err := svc.CreatePlanVersion(ctx, application.CreatePlanVersionCommand{ID: "plan-v1", DefinitionID: "plan", PolicyVersion: "policy-v1", Number: 1, Spec: commercePlanSpec(), EffectiveFrom: commerceBaseTime})
	if err != nil {
		t.Fatal(err)
	}
	plan, err = svc.ActivatePlanVersion(ctx, plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, period, err := svc.StartSubscription(ctx, application.StartSubscriptionCommand{
		ID: "sub-1", TenantID: "tenant-1", PlanVersionID: plan.ID, PeriodID: "period-1",
		State: domain.SubscriptionActive, PeriodStart: commerceBaseTime, PeriodEnd: commerceBaseTime.Add(31 * 24 * time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	return &postgresCommerceFixture{db: db, store: store, clock: clock, ids: ids, svc: svc, plan: plan, period: period}
}

func TestPostgres_CommerceMigrationsCleanInstallAndUpgrade(t *testing.T) {
	db, store := migratedCommerceStore(t)
	ctx := context.Background()
	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("idempotent migration: %v", err)
	}
	var count int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM commerce.schema_migrations`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 3 {
		t.Fatalf("migration count=%d want=3", count)
	}
	for _, table := range []string{"plan_definitions", "plan_versions", "subscriptions", "billing_periods", "commercial_accounts", "quota_reservations", "usage_events", "idempotency", "outbox", "audit", "reconciliation_alerts"} {
		var exists bool
		if err := db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_schema='commerce' AND table_name=$1)`, table).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		if !exists {
			t.Fatalf("missing table commerce.%s", table)
		}
	}
}

func TestPostgres_CommerceSubscriptionPeriodAccountAndOutboxAreAtomic(t *testing.T) {
	db, store := migratedCommerceStore(t)
	ctx := context.Background()
	now := commerceBaseTime
	def, _ := domain.NewPlanDefinition("plan", "Developer", now)
	pv, _ := domain.NewPlanVersion("plan-v1", def.ID, "policy-v1", 1, commercePlanSpec(), now, now)
	_ = pv.Activate(now)
	sub, _ := domain.NewSubscription("sub", "tenant", pv.ID, domain.SubscriptionActive, time.Time{}, now)
	period, _ := domain.NewBillingPeriod("period", "tenant", sub.ID, pv.ID, now, now.Add(24*time.Hour), now)
	account, _ := domain.NewCommercialAccount("tenant", commercev1.CommercialActive, now)
	sentinel := errors.New("rollback")

	err := store.Transact(ctx, func(tx application.Tx) error {
		if err := tx.InsertPlanDefinition(def); err != nil {
			return err
		}
		if err := tx.InsertPlanVersion(pv); err != nil {
			return err
		}
		if err := tx.InsertSubscription(sub); err != nil {
			return err
		}
		if err := tx.InsertBillingPeriod(period); err != nil {
			return err
		}
		if err := tx.InsertCommercialAccount(account); err != nil {
			return err
		}
		if err := tx.AppendOutbox(application.OutboxRecord{ID: "evt", TenantID: "tenant", Topic: "commerce.subscription.started", AggregateID: sub.ID, Payload: []byte(`{"subscription_id":"sub"}`), CreatedAt: now}); err != nil {
			return err
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("rollback error=%v", err)
	}
	for _, table := range []string{"plan_definitions", "plan_versions", "subscriptions", "billing_periods", "commercial_accounts", "outbox"} {
		var n int
		if err := db.QueryRowContext(ctx, `SELECT count(*) FROM commerce.`+table).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Fatalf("commerce.%s count after rollback=%d", table, n)
		}
	}

	if err := store.Transact(ctx, func(tx application.Tx) error {
		if err := tx.InsertPlanDefinition(def); err != nil {
			return err
		}
		if err := tx.InsertPlanVersion(pv); err != nil {
			return err
		}
		if err := tx.InsertSubscription(sub); err != nil {
			return err
		}
		if err := tx.InsertBillingPeriod(period); err != nil {
			return err
		}
		if err := tx.InsertCommercialAccount(account); err != nil {
			return err
		}
		return tx.AppendOutbox(application.OutboxRecord{ID: "evt", TenantID: "tenant", Topic: "commerce.subscription.started", AggregateID: sub.ID, Payload: []byte(`{"subscription_id":"sub"}`), CreatedAt: now})
	}); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"plan_definitions", "plan_versions", "subscriptions", "billing_periods", "commercial_accounts", "outbox"} {
		var n int
		if err := db.QueryRowContext(ctx, `SELECT count(*) FROM commerce.`+table).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Fatalf("commerce.%s count after commit=%d want=1", table, n)
		}
	}
}

func TestPostgres_CommerceConcurrentQuotaReservationsCannotOversubscribe(t *testing.T) {
	f := newPostgresCommerceFixture(t)
	const workers = 24
	start := make(chan struct{})
	var wg sync.WaitGroup
	var mu sync.Mutex
	success, rejected := 0, 0
	errs := make([]error, 0)
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		i := i
		go func() {
			defer wg.Done()
			<-start
			_, err := f.svc.Reserve(context.Background(), commercev1.QuotaRequest{TenantID: "tenant-1", Resource: "runtime.units", Quantity: 1, IdempotencyKey: fmt.Sprintf("quota-%02d", i), At: f.clock.Now(), ExpiresAt: f.clock.Now().Add(time.Hour)})
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				success++
			case domain.HasCode(err, domain.CodeQuotaExceeded):
				rejected++
			default:
				errs = append(errs, err)
			}
		}()
	}
	close(start)
	wg.Wait()
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if success != 2 || rejected != workers-2 {
		t.Fatalf("success=%d rejected=%d", success, rejected)
	}
	var quantity int64
	if err := f.db.QueryRow(`SELECT coalesce(sum(quantity),0) FROM commerce.quota_reservations WHERE tenant_id='tenant-1' AND resource='runtime.units' AND state IN ('RESERVED','COMMITTED')`).Scan(&quantity); err != nil {
		t.Fatal(err)
	}
	if quantity != 2 {
		t.Fatalf("reserved quantity=%d", quantity)
	}
}

func TestPostgres_CommerceConcurrentUsageIdempotencyUsesSingleWinner(t *testing.T) {
	f := newPostgresCommerceFixture(t)
	const workers = 20
	start := make(chan struct{})
	var wg sync.WaitGroup
	errCh := make(chan error, workers)
	wg.Add(workers)
	for range workers {
		go func() {
			defer wg.Done()
			<-start
			errCh <- f.svc.Append(context.Background(), commercev1.UsageEvent{TenantID: "tenant-1", PeriodID: f.period.ID, ResourceType: "application", ResourceID: "app-1", Meter: commercev1.MeterRuntimeUnitSeconds, Quantity: 3600, IdempotencyKey: "same-usage", OccurredAt: commerceBaseTime.Add(time.Hour), WindowStart: commerceBaseTime, WindowEnd: commerceBaseTime.Add(time.Hour)})
		}()
	}
	close(start)
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Errorf("append: %v", err)
		}
	}
	for table, want := range map[string]int{"usage_events": 1, "idempotency": 1} {
		var n int
		if err := f.db.QueryRow(`SELECT count(*) FROM commerce.` + table).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != want {
			t.Fatalf("%s count=%d want=%d", table, n, want)
		}
	}
	err := f.svc.Append(context.Background(), commercev1.UsageEvent{TenantID: "tenant-1", PeriodID: f.period.ID, ResourceType: "application", ResourceID: "app-1", Meter: commercev1.MeterRuntimeUnitSeconds, Quantity: 3601, IdempotencyKey: "same-usage", OccurredAt: commerceBaseTime.Add(time.Hour), WindowStart: commerceBaseTime, WindowEnd: commerceBaseTime.Add(time.Hour)})
	if !domain.HasCode(err, domain.CodeConflict) {
		t.Fatalf("altered payload error=%v", err)
	}
}

func TestPostgres_CommerceOptimisticLockPreventsLostAccountUpdate(t *testing.T) {
	f := newPostgresCommerceFixture(t)
	ctx := context.Background()
	var first, second domain.CommercialAccount
	if err := f.store.Transact(ctx, func(tx application.Tx) error {
		var ok bool
		first, ok = tx.GetCommercialAccount("tenant-1")
		if !ok {
			return errors.New("first missing")
		}
		second, ok = tx.GetCommercialAccount("tenant-1")
		if !ok {
			return errors.New("second missing")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := first.Transition(commercev1.CommercialGrace, f.clock.Now()); err != nil {
		t.Fatal(err)
	}
	if err := second.Transition(commercev1.CommercialSuspended, f.clock.Now()); err != nil {
		t.Fatal(err)
	}
	if err := f.store.Transact(ctx, func(tx application.Tx) error { return tx.UpdateCommercialAccount(first, 1) }); err != nil {
		t.Fatal(err)
	}
	err := f.store.Transact(ctx, func(tx application.Tx) error { return tx.UpdateCommercialAccount(second, 1) })
	if !domain.HasCode(err, domain.CodeStaleVersion) {
		t.Fatalf("stale update error=%v", err)
	}
}

func TestPostgres_CommerceActivePlanAndPeriodPriceVersionAreImmutable(t *testing.T) {
	f := newPostgresCommerceFixture(t)
	if _, err := f.db.Exec(`UPDATE commerce.plan_versions SET currency='USD', spec_hash=repeat('a',64) WHERE id='plan-v1'`); err == nil {
		t.Fatal("active plan mutation succeeded")
	}
	if _, err := f.db.Exec(`UPDATE commerce.billing_periods SET plan_version_id='another' WHERE id='period-1'`); err == nil {
		t.Fatal("billing-period price version mutation succeeded")
	}
}

func TestPostgres_CommerceUsageAuditAndIdempotencyAreAppendOnly(t *testing.T) {
	f := newPostgresCommerceFixture(t)
	if err := f.svc.Append(context.Background(), commercev1.UsageEvent{TenantID: "tenant-1", PeriodID: f.period.ID, ResourceType: "application", ResourceID: "app-1", Meter: commercev1.MeterRuntimeUnitSeconds, Quantity: 1, IdempotencyKey: "append-only", OccurredAt: commerceBaseTime.Add(time.Hour), WindowStart: commerceBaseTime, WindowEnd: commerceBaseTime.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`UPDATE commerce.usage_events SET quantity=2 WHERE idempotency_key='append-only'`,
		`DELETE FROM commerce.usage_events WHERE idempotency_key='append-only'`,
		`UPDATE commerce.audit SET action='tampered'`,
		`DELETE FROM commerce.audit`,
		`UPDATE commerce.idempotency SET fingerprint=repeat('a',64)`,
	} {
		if _, err := f.db.Exec(statement); err == nil {
			t.Fatalf("append-only mutation succeeded: %s", statement)
		}
	}
}

func TestPostgres_CommercePayloadConsistencyRejectsColumnDrift(t *testing.T) {
	f := newPostgresCommerceFixture(t)
	if _, err := f.db.Exec(`ALTER TABLE commerce.plan_definitions DISABLE TRIGGER commerce_plan_definition_guard`); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = f.db.Exec(`ALTER TABLE commerce.plan_definitions ENABLE TRIGGER commerce_plan_definition_guard`)
	}()
	if _, err := f.db.Exec(`UPDATE commerce.plan_definitions SET name='Drift' WHERE id='plan'`); err == nil {
		t.Fatal("column/payload drift succeeded")
	}
}

func TestPostgres_CommerceSchemaUsesNoFloatingMoneyColumns(t *testing.T) {
	db, _ := migratedCommerceStore(t)
	rows, err := db.Query(`SELECT table_name,column_name,data_type FROM information_schema.columns WHERE table_schema='commerce' AND data_type IN ('real','double precision','numeric','decimal','money')`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var found []string
	for rows.Next() {
		var table, column, typ string
		if err := rows.Scan(&table, &column, &typ); err != nil {
			t.Fatal(err)
		}
		found = append(found, table+"."+column+":"+typ)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(found) != 0 {
		t.Fatalf("floating/money columns: %v", found)
	}
}

func TestPostgres_CommerceInvoicePreviewIsDeterministic(t *testing.T) {
	f := newPostgresCommerceFixture(t)
	ctx := context.Background()
	for i, quantity := range []int64{1200, 2400} {
		if err := f.svc.Append(ctx, commercev1.UsageEvent{TenantID: "tenant-1", PeriodID: f.period.ID, ResourceType: "application", ResourceID: "app-1", Meter: commercev1.MeterRuntimeUnitSeconds, Quantity: quantity, IdempotencyKey: fmt.Sprintf("invoice-%d", i), OccurredAt: commerceBaseTime.Add(time.Duration(i+1) * time.Hour), WindowStart: commerceBaseTime.Add(time.Duration(i) * time.Hour), WindowEnd: commerceBaseTime.Add(time.Duration(i+1) * time.Hour)}); err != nil {
			t.Fatal(err)
		}
	}
	first, err := f.svc.PreviewInvoice(ctx, "tenant-1", f.period.ID)
	if err != nil {
		t.Fatal(err)
	}
	second, err := f.svc.PreviewInvoice(ctx, "tenant-1", f.period.ID)
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprintf("%#v", first) != fmt.Sprintf("%#v", second) {
		t.Fatalf("previews differ\nfirst=%#v\nsecond=%#v", first, second)
	}
	if first.TotalMinorUnits != 1 {
		t.Fatalf("total=%d want=1", first.TotalMinorUnits)
	}
}
