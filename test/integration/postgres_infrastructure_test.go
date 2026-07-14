//go:build postgres_integration

package integration_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	infraapp "github.com/keir-research/ai-native-paas/internal/infrastructure/application"
	infrapg "github.com/keir-research/ai-native-paas/internal/infrastructure/postgres"
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

func TestPostgres_InfrastructureApprovalIsExactAndSingleUse(t *testing.T) {
	db, store := migratedInfrastructureStore(t)
	now := time.Date(2026, 7, 14, 10, 0, 0, 0, time.UTC)
	service := &infraapp.Service{
		Store: store, Clock: infrastructureClock{now: now}, IDs: &infrastructureIDs{},
		Prices: infraapp.PriceBook{
			RateCard: commercev2.RateCard{RateCardID: "beta-pg", Version: "1", MarkupBasisPoints: 1000, Currency: "RUB", PriceSnapshotID: "prices-pg"},
			Prices:   map[string]infraapp.UnitPrice{"twc_server": {Meter: "server.month", Unit: "server-month", ProviderMinorPerQuantity: 1000, Known: true}},
		},
	}
	command := infraapp.PlanCommand{
		TenantID: "tenant-pg", ProjectID: "project-pg", ActorID: "agent-pg", WorkspaceID: "workspace-pg", Target: "production",
		SourceSHA: strings.Repeat("a", 40), IdempotencyKey: "plan-pg",
		ArtifactDigest: "sha256:" + strings.Repeat("b", 64), StateGeneration: 3,
		PlanJSON: []byte(`{"resource_changes":[{"address":"twc_server.app","provider_name":"timeweb","type":"twc_server","change":{"actions":["create"]}}]}`),
	}
	plan, err := service.Plan(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := service.Plan(context.Background(), command)
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
	if _, err = service.AuthorizeApply(context.Background(), infraapp.ApplyCommand{TenantID: "tenant-pg", ProjectID: "project-pg", Authorization: mismatch}); !errors.Is(err, infraapp.ErrPermissionDenied) {
		t.Fatalf("mismatched plan consumed approval: %v", err)
	}

	const workers = 8
	start := make(chan struct{})
	var winners atomic.Int64
	var group sync.WaitGroup
	group.Add(workers)
	for index := 0; index < workers; index++ {
		go func() {
			defer group.Done()
			<-start
			if _, applyErr := service.AuthorizeApply(context.Background(), infraapp.ApplyCommand{TenantID: "tenant-pg", ProjectID: "project-pg", Authorization: authorization}); applyErr == nil {
				winners.Add(1)
			}
		}()
	}
	close(start)
	group.Wait()
	if winners.Load() != 1 {
		t.Fatalf("exact-plan authorization winners=%d want=1", winners.Load())
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
