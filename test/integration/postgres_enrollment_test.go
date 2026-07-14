//go:build postgres_integration

package integration_test

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/keir-research/ai-native-paas/internal/agent/enrollment"
	enrollmentpostgres "github.com/keir-research/ai-native-paas/internal/agent/enrollment/postgres"
)

type enrollmentClock struct{ now time.Time }

func (c enrollmentClock) Now() time.Time { return c.now }

type enrollmentIDs struct {
	mu   sync.Mutex
	next int
}

func (i *enrollmentIDs) New(prefix string) string {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.next++
	return fmt.Sprintf("%s-%d", prefix, i.next)
}

func migratedEnrollmentStore(t *testing.T) (*sql.DB, *enrollmentpostgres.Store) {
	t.Helper()
	db := openPostgres(t)
	if _, err := db.Exec(`DROP SCHEMA IF EXISTS agent_enrollment CASCADE`); err != nil {
		t.Fatal(err)
	}
	store, err := enrollmentpostgres.New(db)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatalf("idempotent migration: %v", err)
	}
	return db, store
}

func TestPostgres_AgentEnrollmentIsSingleUseAndStoresOnlyTokenHashes(t *testing.T) {
	db, store := migratedEnrollmentStore(t)
	clock := enrollmentClock{now: time.Date(2026, 7, 14, 4, 15, 0, 0, time.UTC)}
	service := &enrollment.Service{
		Store:   store,
		Clock:   clock,
		IDs:     &enrollmentIDs{},
		Secrets: enrollment.CryptoSecrets{},
		Signer:  enrollment.HMACSigner{Key: []byte("0123456789abcdef0123456789abcdef")},
	}
	binding := enrollment.Binding{
		TenantID: "tenant-1", ProjectID: "project-1", UserID: "user-1", AgentID: "agent-1",
		Scopes: []string{"agent.tool:project_get", "agent.tool:workspace_create"},
	}
	issued, err := service.Issue(context.Background(), binding)
	if err != nil {
		t.Fatal(err)
	}
	var plaintextRows int
	if err := db.QueryRow(`SELECT count(*) FROM agent_enrollment.enrollments WHERE token_hash=$1`, issued.Token).Scan(&plaintextRows); err != nil {
		t.Fatal(err)
	}
	if plaintextRows != 0 {
		t.Fatal("plaintext enrollment token was persisted")
	}

	const workers = 16
	var group sync.WaitGroup
	credentials := make(chan enrollment.RefreshCredential, workers)
	group.Add(workers)
	for i := 0; i < workers; i++ {
		go func(i int) {
			defer group.Done()
			credential, exchangeErr := service.Exchange(context.Background(), issued.Token, binding.AgentID, fmt.Sprintf("ssh-ed25519 public-key-%d", i))
			if exchangeErr == nil {
				credentials <- credential
			}
		}(i)
	}
	group.Wait()
	close(credentials)
	var winners []enrollment.RefreshCredential
	for credential := range credentials {
		winners = append(winners, credential)
	}
	if len(winners) != 1 {
		t.Fatalf("enrollment exchange winners=%d want=1", len(winners))
	}
	var refreshPlaintextRows int
	if err := db.QueryRow(`SELECT count(*) FROM agent_enrollment.refresh_credentials WHERE token_hash=$1`, winners[0].RefreshToken).Scan(&refreshPlaintextRows); err != nil {
		t.Fatal(err)
	}
	if refreshPlaintextRows != 0 {
		t.Fatal("plaintext refresh credential was persisted")
	}
	access, claims, err := service.Access(context.Background(), winners[0].RefreshToken)
	if err != nil || access == "" || claims.ProjectID != binding.ProjectID {
		t.Fatalf("access credential: claims=%#v err=%v", claims, err)
	}
	if err := service.Revoke(context.Background(), winners[0].RefreshToken); err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.Access(context.Background(), winners[0].RefreshToken); err == nil {
		t.Fatal("revoked refresh credential minted an access token")
	}
}
