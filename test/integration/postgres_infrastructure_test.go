//go:build postgres_integration

package integration_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	infraapp "github.com/keir-research/ai-native-paas/internal/infrastructure/application"
	infrapg "github.com/keir-research/ai-native-paas/internal/infrastructure/postgres"
	"github.com/keir-research/ai-native-paas/internal/workspace"
	commercev2 "github.com/keir-research/ai-native-paas/pkg/contracts/commerce/v2"
	infrastructurev1 "github.com/keir-research/ai-native-paas/pkg/contracts/infrastructure/v1"
)

type infrastructureClock struct{ now time.Time }

func (c infrastructureClock) Now() time.Time { return c.now }

type infrastructureIDs struct {
	mu sync.Mutex
	n  int
}

func (i *infrastructureIDs) New(prefix string) string {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.n++
	return fmt.Sprintf("%s-pg-%d", prefix, i.n)
}

func migratedInfrastructureStore(t *testing.T) (*sql.DB, *infrapg.Store) {
	t.Helper()
	db := openPostgres(t)
	if _, err := db.Exec(`DROP SCHEMA IF EXISTS infrastructure CASCADE`); err != nil {
		t.Fatal(err)
	}
	store := &infrapg.Store{DB: db}
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatalf("idempotent infrastructure migration: %v", err)
	}
	return db, store
}

func TestAgent_ApprovalIsSingleUseAndBoundToCanonicalPlan(t *testing.T) {
	db, store := migratedInfrastructureStore(t)
	now := time.Date(2026, 7, 14, 10, 0, 0, 0, time.UTC)
	service := &infraapp.Service{
		Store: store, Clock: infrastructureClock{now: now}, IDs: &infrastructureIDs{},
		Prices: infraapp.PriceBook{
			RateCard: commercev2.RateCard{RateCardID: "beta-pg", Version: "1", MarkupBasisPoints: 1000, Currency: "RUB", PriceSnapshotID: "prices-pg"},
			Prices:   map[string]infraapp.UnitPrice{"twc_server": {Meter: "server.month", Unit: "server-month", ProviderMinorPerQuantity: 1000, Known: true}},
		},
	}
	receipt := infrastructurev1.AgentPlanReceipt{
		SessionID: "session-pg", ExecutionSessionID: "session-pg", CommandID: "command-pg",
		ArtifactDigest: "sha256:" + strings.Repeat("b", 64), CapturedAt: now,
		PlanJSON: []byte(`{"resource_changes":[{"address":"twc_server.app","provider_name":"timeweb","type":"twc_server","change":{"actions":["create"]}}]}`),
		RetainedResources: []infrastructurev1.RetainedResource{{
			Address: "cozystack_postgres.primary", Provider: "cozystack", ResourceType: "cozystack_postgres",
			ExternalID: "postgres-primary", Policy: "platform.yaml/v2:retain", Reason: "production data retention policy",
		}},
	}
	scope := workspace.PlanReceiptScope{
		TenantID: "tenant-pg", ProjectID: "project-pg", WorkspaceID: "workspace-pg", TaskID: "task-pg", CommandID: "command-pg", ActorID: "agent-pg",
	}
	if err := store.PutPlanReceipt(context.Background(), scope, receipt); err != nil {
		t.Fatal(err)
	}
	changedReceipt := receipt
	changedReceipt.RetainedResources = append([]infrastructurev1.RetainedResource(nil), receipt.RetainedResources...)
	changedReceipt.RetainedResources[0].Reason = "changed retention basis"
	if err := store.PutPlanReceipt(context.Background(), scope, changedReceipt); !errors.Is(err, infraapp.ErrConflict) {
		t.Fatalf("PostgreSQL receipt replay changed retention decision: %v", err)
	}
	command := infraapp.ReceiptPlanCommand{
		TenantID: "tenant-pg", ProjectID: "project-pg", ActorID: "agent-pg", WorkspaceID: "workspace-pg", CommandID: "command-pg", Target: "production",
		SourceSHA: strings.Repeat("a", 40), IdempotencyKey: "plan-pg", StateGeneration: 3,
	}
	plan, err := service.PlanFromReceipt(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Summary.RetainedResources) != 1 || plan.Summary.RetainedResources[0].ExternalID != "postgres-primary" {
		t.Fatalf("retention was not durable through PostgreSQL: %+v", plan.Summary.RetainedResources)
	}
	if _, err = db.Exec(`UPDATE infrastructure.plans SET retained_resources='[]'::jsonb WHERE id=$1`, plan.Summary.PlanID); err == nil {
		t.Fatal("durable plan retention was mutable")
	}
	if _, err = db.Exec(`UPDATE infrastructure.plan_receipts SET retained_resources='[]'::jsonb WHERE command_id=$1`, receipt.CommandID); err == nil {
		t.Fatal("authenticated receipt retention was mutable")
	}
	replayed, err := service.PlanFromReceipt(context.Background(), command)
	if err != nil || replayed.Summary.PlanID != plan.Summary.PlanID {
		t.Fatalf("durable idempotent plan: replay=%#v err=%v", replayed, err)
	}
	grant, err := service.GrantApproval(context.Background(), infraapp.GrantApprovalCommand{
		TenantID: "tenant-pg", ProjectID: "project-pg", PlanID: plan.Summary.PlanID,
		ActorID: "agent-pg", ApproverUserID: "human-pg", ExpiresAt: now.Add(10 * time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	authorization := infrastructurev1.ApplyAuthorization{
		PlanID: plan.Summary.PlanID, PlanHash: plan.Summary.PlanHash,
		EstimateVersion: plan.Estimate.Version, ReservationID: plan.Reservation.ReservationID,
		ApprovalGrantID: grant.GrantID, Target: "production", ActorID: "agent-pg",
		ExpiresAt: now.Add(5 * time.Minute),
	}
	mismatch := authorization
	mismatch.PlanHash = "sha256:" + strings.Repeat("c", 64)
	if _, err = service.AuthorizeApply(context.Background(), infraapp.ApplyCommand{TenantID: "tenant-pg", ProjectID: "project-pg", IdempotencyKey: "apply-pg", Authorization: mismatch}); !errors.Is(err, infraapp.ErrPermissionDenied) {
		t.Fatalf("mismatched plan consumed approval: %v", err)
	}
	mismatch = authorization
	mismatch.ActorID = "another-agent"
	if _, err = service.AuthorizeApply(context.Background(), infraapp.ApplyCommand{TenantID: "tenant-pg", ProjectID: "project-pg", IdempotencyKey: "apply-pg", Authorization: mismatch}); !errors.Is(err, infraapp.ErrPermissionDenied) {
		t.Fatalf("mismatched agent consumed approval: %v", err)
	}

	const workers = 2
	start := make(chan struct{})
	type applyResult struct {
		key string
		err error
	}
	results := make(chan applyResult, workers)
	var group sync.WaitGroup
	group.Add(workers)
	for index := 0; index < workers; index++ {
		index := index
		go func() {
			defer group.Done()
			<-start
			key := fmt.Sprintf("apply-pg-%d", index)
			_, applyErr := service.AuthorizeApply(context.Background(), infraapp.ApplyCommand{TenantID: "tenant-pg", ProjectID: "project-pg", IdempotencyKey: key, Authorization: authorization})
			results <- applyResult{key: key, err: applyErr}
		}()
	}
	close(start)
	group.Wait()
	close(results)
	winners, conflicts, winningKey := 0, 0, ""
	for result := range results {
		switch {
		case result.err == nil:
			winners++
			winningKey = result.key
		case errors.Is(result.err, infraapp.ErrConflict):
			conflicts++
		default:
			t.Fatalf("unexpected concurrent apply result for %s: %v", result.key, result.err)
		}
	}
	if winners != 1 || conflicts != 1 {
		t.Fatalf("single-use race winners=%d conflicts=%d", winners, conflicts)
	}
	service.Clock = infrastructureClock{now: now.Add(time.Hour)}
	if _, err := service.AuthorizeApply(context.Background(), infraapp.ApplyCommand{TenantID: "tenant-pg", ProjectID: "project-pg", IdempotencyKey: winningKey, Authorization: authorization}); err != nil {
		t.Fatalf("exact lost-response retry failed after authorization expiry: %v", err)
	}
	if _, err := service.AuthorizeApply(context.Background(), infraapp.ApplyCommand{TenantID: "tenant-pg", ProjectID: "project-pg", IdempotencyKey: "different-apply", Authorization: authorization}); !errors.Is(err, infraapp.ErrConflict) {
		t.Fatalf("approval reused by a different apply command: %v", err)
	}
	var consumed, started int
	if err := db.QueryRow(`SELECT count(*) FROM infrastructure.approval_grants WHERE id=$1 AND consumed_at IS NOT NULL`, grant.GrantID).Scan(&consumed); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM infrastructure.plans WHERE id=$1 AND apply_started_at IS NOT NULL`, plan.Summary.PlanID).Scan(&started); err != nil {
		t.Fatal(err)
	}
	if consumed != 1 || started != 1 {
		t.Fatalf("atomic apply state approval=%d plan=%d", consumed, started)
	}
	if _, err := db.Exec(`UPDATE infrastructure.approval_grants SET actor_id='attacker' WHERE id=$1`, grant.GrantID); err == nil {
		t.Fatal("approval identity mutation was accepted")
	}
}

func TestPostgres_InfrastructureMigration005BackfillsRetentionArrays(t *testing.T) {
	db, store := migratedInfrastructureStore(t)
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	service := &infraapp.Service{
		Store: store, Clock: infrastructureClock{now: now}, IDs: &infrastructureIDs{},
		Prices: infraapp.PriceBook{
			RateCard: commercev2.RateCard{RateCardID: "beta-retention-upgrade", Version: "1", MarkupBasisPoints: 1000, Currency: "RUB", PriceSnapshotID: "prices-retention-upgrade"},
			Prices:   map[string]infraapp.UnitPrice{"twc_server": {Meter: "server.month", Unit: "server-month", ProviderMinorPerQuantity: 1000, Known: true}},
		},
	}
	receipt := infrastructurev1.AgentPlanReceipt{
		SessionID: "session-retention-upgrade", ExecutionSessionID: "session-retention-upgrade", CommandID: "command-retention-upgrade",
		ArtifactDigest: "sha256:" + strings.Repeat("f", 64), CapturedAt: now,
		PlanJSON: []byte(`{"resource_changes":[{"address":"twc_server.legacy","provider_name":"timeweb","type":"twc_server","change":{"actions":["create"]}}]}`),
	}
	if err := store.PutPlanReceipt(context.Background(), workspace.PlanReceiptScope{
		TenantID: "tenant-retention-upgrade", ProjectID: "project-retention-upgrade", WorkspaceID: "workspace-retention-upgrade",
		TaskID: "task-retention-upgrade", CommandID: receipt.CommandID, ActorID: "agent-retention-upgrade",
	}, receipt); err != nil {
		t.Fatal(err)
	}
	plan, err := service.PlanFromReceipt(context.Background(), infraapp.ReceiptPlanCommand{
		TenantID: "tenant-retention-upgrade", ProjectID: "project-retention-upgrade", ActorID: "agent-retention-upgrade",
		WorkspaceID: "workspace-retention-upgrade", CommandID: receipt.CommandID, Target: "staging",
		SourceSHA: strings.Repeat("a", 40), IdempotencyKey: "plan-retention-upgrade", StateGeneration: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`
		ALTER TABLE infrastructure.plans DROP COLUMN retained_resources;
		ALTER TABLE infrastructure.plan_receipts DROP COLUMN retained_resources;
		DELETE FROM infrastructure.schema_migrations WHERE version='005_retained_resources.sql';
	`); err != nil {
		t.Fatal(err)
	}
	if err = store.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate schema 004 with existing plan and receipt to 005: %v", err)
	}
	var planType, receiptType string
	var planCount, receiptCount int
	if err = db.QueryRow(`SELECT jsonb_typeof(retained_resources),jsonb_array_length(retained_resources) FROM infrastructure.plans WHERE id=$1`, plan.Summary.PlanID).Scan(&planType, &planCount); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(`SELECT jsonb_typeof(retained_resources),jsonb_array_length(retained_resources) FROM infrastructure.plan_receipts WHERE command_id=$1`, receipt.CommandID).Scan(&receiptType, &receiptCount); err != nil {
		t.Fatal(err)
	}
	if planType != "array" || receiptType != "array" || planCount != 0 || receiptCount != 0 {
		t.Fatalf("migration 005 backfill plan=%s/%d receipt=%s/%d", planType, planCount, receiptType, receiptCount)
	}
	if _, err = store.GetPlan(context.Background(), "tenant-retention-upgrade", "project-retention-upgrade", plan.Summary.PlanID); err != nil {
		t.Fatalf("read plan after migration 005: %v", err)
	}
	if _, err = store.GetPlanReceipt(context.Background(), "tenant-retention-upgrade", "project-retention-upgrade", receipt.CommandID); err != nil {
		t.Fatalf("read receipt after migration 005: %v", err)
	}
}

func TestPostgres_InfrastructureMigration004BackfillsStartedApply(t *testing.T) {
	db, store := migratedInfrastructureStore(t)
	now := time.Date(2026, 7, 14, 11, 0, 0, 0, time.UTC)
	service := &infraapp.Service{
		Store: store, Clock: infrastructureClock{now: now}, IDs: &infrastructureIDs{},
		Prices: infraapp.PriceBook{
			RateCard: commercev2.RateCard{RateCardID: "beta-upgrade", Version: "1", MarkupBasisPoints: 1000, Currency: "RUB", PriceSnapshotID: "prices-upgrade"},
			Prices:   map[string]infraapp.UnitPrice{"twc_server": {Meter: "server.month", Unit: "server-month", ProviderMinorPerQuantity: 1000, Known: true}},
		},
	}
	receipt := infrastructurev1.AgentPlanReceipt{
		SessionID: "session-upgrade", ExecutionSessionID: "session-upgrade", CommandID: "command-upgrade",
		ArtifactDigest: "sha256:" + strings.Repeat("d", 64), CapturedAt: now,
		PlanJSON: []byte(`{"resource_changes":[{"address":"twc_server.legacy","provider_name":"timeweb","type":"twc_server","change":{"actions":["create"]}}]}`),
	}
	if err := store.PutPlanReceipt(context.Background(), workspace.PlanReceiptScope{
		TenantID: "tenant-upgrade", ProjectID: "project-upgrade", WorkspaceID: "workspace-upgrade", TaskID: "task-upgrade", CommandID: "command-upgrade", ActorID: "agent-upgrade",
	}, receipt); err != nil {
		t.Fatal(err)
	}
	plan, err := service.PlanFromReceipt(context.Background(), infraapp.ReceiptPlanCommand{
		TenantID: "tenant-upgrade", ProjectID: "project-upgrade", ActorID: "agent-upgrade", WorkspaceID: "workspace-upgrade", CommandID: "command-upgrade",
		Target: "staging", SourceSHA: strings.Repeat("e", 40), IdempotencyKey: "plan-upgrade", StateGeneration: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	authorization := infrastructurev1.ApplyAuthorization{
		PlanID: plan.Summary.PlanID, PlanHash: plan.Summary.PlanHash, EstimateVersion: plan.Estimate.Version,
		ReservationID: plan.Reservation.ReservationID, Target: "staging", ActorID: "agent-upgrade", ExpiresAt: now.Add(5 * time.Minute),
	}
	if _, err = service.AuthorizeApply(context.Background(), infraapp.ApplyCommand{
		TenantID: "tenant-upgrade", ProjectID: "project-upgrade", IdempotencyKey: "apply-upgrade", Authorization: authorization,
	}); err != nil {
		t.Fatal(err)
	}

	// Reconstruct the schema immediately before migration 004 while retaining
	// the already-started apply row that made the original migration fail.
	if _, err = db.Exec(`
		ALTER TABLE infrastructure.plans DROP CONSTRAINT infrastructure_apply_binding_complete;
		ALTER TABLE infrastructure.plans DROP COLUMN apply_idempotency_key, DROP COLUMN apply_authorization_fingerprint;
		DELETE FROM infrastructure.schema_migrations WHERE version='004_idempotent_apply_dispatch.sql';
	`); err != nil {
		t.Fatal(err)
	}
	if err = store.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate schema 003 with a started apply to 004: %v", err)
	}
	var key, fingerprint string
	if err = db.QueryRow(`SELECT apply_idempotency_key,apply_authorization_fingerprint FROM infrastructure.plans WHERE id=$1`, plan.Summary.PlanID).Scan(&key, &fingerprint); err != nil {
		t.Fatal(err)
	}
	if key != "migration-legacy/"+plan.Summary.PlanID || fingerprint != "sha256:"+strings.Repeat("0", 64) {
		t.Fatalf("unexpected migration binding key=%q fingerprint=%q", key, fingerprint)
	}
	if _, err = service.AuthorizeApply(context.Background(), infraapp.ApplyCommand{
		TenantID: "tenant-upgrade", ProjectID: "project-upgrade", IdempotencyKey: "apply-upgrade", Authorization: authorization,
	}); !errors.Is(err, infraapp.ErrConflict) {
		t.Fatalf("pre-004 apply was replayed without a verifiable binding: %v", err)
	}
}
