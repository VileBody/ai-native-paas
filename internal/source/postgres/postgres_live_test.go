//go:build integration_postgres

package postgres_test

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/stdlib"
	"github.com/keir-research/ai-native-paas/internal/source/application"
	"github.com/keir-research/ai-native-paas/internal/source/domain"
	sourcepostgres "github.com/keir-research/ai-native-paas/internal/source/postgres"
)

func init() {
	sql.Register("source_test_pgx", stdlib.GetDefaultDriver())
}

var liveMu sync.Mutex

func dsn(t *testing.T) string {
	t.Helper()
	v := os.Getenv("TEST_POSTGRES_DSN")
	if v == "" {
		t.Skip("TEST_POSTGRES_DSN not set")
	}
	if _, err := exec.LookPath("psql"); err != nil {
		t.Skip("psql unavailable")
	}
	return v
}
func rootDir(t *testing.T) string {
	t.Helper()
	d, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err = os.Stat(filepath.Join(d, "go.mod")); err == nil {
			return d
		}
		p := filepath.Dir(d)
		if p == d {
			t.Fatal("go.mod not found")
		}
		d = p
	}
}
func psql(t *testing.T, sql string) (string, error) {
	t.Helper()
	cmd := exec.Command("psql", dsn(t), "-X", "--no-psqlrc", "-v", "ON_ERROR_STOP=1", "-At", "-F", "\t")
	cmd.Stdin = strings.NewReader(sql)
	raw, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(raw)), func() error {
		if err != nil {
			return fmt.Errorf("psql: %w: %s", err, raw)
		}
		return nil
	}()
}
func reset(t *testing.T) {
	t.Helper()
	liveMu.Lock()
	t.Cleanup(liveMu.Unlock)
	if _, err := psql(t, "DROP SCHEMA IF EXISTS source CASCADE;"); err != nil {
		t.Fatal(err)
	}
	names, err := filepath.Glob(filepath.Join(rootDir(t), "migrations", "source", "*.sql"))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range names {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = psql(t, string(raw)); err != nil {
			t.Fatalf("migration %s: %v", filepath.Base(path), err)
		}
	}
}
func seedSQL() string {
	return `INSERT INTO source.projects(id,tenant_id,name,slug,version,created_at,updated_at) VALUES('p','t','P','p',1,now(),now()); INSERT INTO source.repositories(id,tenant_id,project_id,provider,provider_namespace_id,provider_project_id,provider_path,web_url,default_branch,state,last_error,correlation_id,version,created_at,updated_at) VALUES('r','t','p','gitlab',7,42,'g/p','','main','READY','','corr',1,now(),now());`
}
func TestPostgres_SourceMigrationsCleanInstallAndUpgrade(t *testing.T) {
	reset(t)
	out, err := psql(t, "SELECT count(*) FROM information_schema.tables WHERE table_schema='source';")
	if err != nil {
		t.Fatal(err)
	}
	n, _ := strconv.Atoi(out)
	if n < 10 {
		t.Fatalf("tables=%d", n)
	}
	names, err := filepath.Glob(filepath.Join(rootDir(t), "migrations", "source", "*.sql"))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range names {
		raw, _ := os.ReadFile(path)
		if _, err = psql(t, string(raw)); err != nil {
			t.Fatalf("idempotent rerun %s: %v", filepath.Base(path), err)
		}
	}
}

func TestPostgres_BranchEnvironmentBindingAndDeletionPersist(t *testing.T) {
	reset(t)
	if _, err := psql(t, seedSQL()+" INSERT INTO source.branch_heads(repository_id,name,commit_sha,environment_id,deleted_at,version) VALUES('r','preview/7',repeat('a',40),'env-7',now(),2);"); err != nil {
		t.Fatal(err)
	}
	out, err := psql(t, "SELECT environment_id || ':' || (deleted_at IS NOT NULL)::text FROM source.branch_heads WHERE repository_id='r' AND name='preview/7';")
	if err != nil || out != "env-7:true" {
		t.Fatalf("out=%q err=%v", out, err)
	}
}

func TestPostgres_BranchEnvironmentStateRoundTripsThroughStore(t *testing.T) {
	reset(t)
	if _, err := psql(t, seedSQL()); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("source_test_pgx", dsn(t))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := &sourcepostgres.Store{DB: db}
	observedAt := time.Date(2026, 7, 14, 14, 0, 0, 0, time.UTC)
	branch, err := domain.NewBranchHead("r", "preview/store")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = branch.BindPreviewEnvironment("env-store"); err != nil {
		t.Fatal(err)
	}
	if _, err = branch.ObserveDeletion("delete-store", observedAt, observedAt); err != nil {
		t.Fatal(err)
	}
	if err = store.Transact(context.Background(), func(tx application.Tx) error { return tx.UpsertBranch(branch, 0) }); err != nil {
		t.Fatal(err)
	}
	var restored domain.BranchHead
	if err = store.Transact(context.Background(), func(tx application.Tx) error {
		var ok bool
		restored, ok = tx.GetBranch("r", "preview/store")
		if !ok {
			return fmt.Errorf("branch missing")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if restored.EnvironmentID != "env-store" || restored.LastEventID != "delete-store" || !restored.DeletedAt.Equal(observedAt) {
		t.Fatalf("restored=%+v", restored)
	}
}
func TestPostgres_BootstrapRevisionRoundTripsThroughStore(t *testing.T) {
	reset(t)
	if _, err := psql(t, seedSQL()); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("source_test_pgx", dsn(t))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := &sourcepostgres.Store{DB: db}
	revision := strings.Repeat("b", 40)
	if err = store.Transact(context.Background(), func(tx application.Tx) error {
		repository, ok := tx.GetRepository("r")
		if !ok {
			return fmt.Errorf("repository missing")
		}
		expected := repository.Version
		if _, err := repository.RecordBootstrapRevision(revision, time.Now()); err != nil {
			return err
		}
		return tx.UpdateRepository(repository, expected)
	}); err != nil {
		t.Fatal(err)
	}
	var restored domain.Repository
	if err = store.Transact(context.Background(), func(tx application.Tx) error {
		var ok bool
		restored, ok = tx.GetRepository("r")
		if !ok {
			return fmt.Errorf("repository missing after update")
		}
		return nil
	}); err != nil || restored.BootstrapRevision != revision {
		t.Fatalf("repository=%+v err=%v", restored, err)
	}
	if _, err = psql(t, "UPDATE source.repositories SET bootstrap_revision=repeat('c',40) WHERE id='r';"); err == nil {
		t.Fatal("bootstrap revision mutation accepted")
	}
	out, err := psql(t, "SELECT bootstrap_revision FROM source.repositories WHERE id='r';")
	if err != nil || out != revision {
		t.Fatalf("bootstrap revision after rejected mutation=%q err=%v", out, err)
	}
}
func TestPostgres_ProjectRepositoryOutboxAtomic(t *testing.T) {
	reset(t)
	script := `BEGIN; INSERT INTO source.projects(id,tenant_id,name,slug,version,created_at,updated_at) VALUES('p','t','P','p',1,now(),now()); INSERT INTO source.repositories(id,tenant_id,project_id,provider,provider_namespace_id,provider_path,web_url,default_branch,state,last_error,correlation_id,version,created_at,updated_at) VALUES('r','t','p','gitlab',7,'','','main','REQUESTED','','corr',1,now(),now()); INSERT INTO source.outbox(id,topic,aggregate_id,payload,created_at) VALUES('e','x','p','{}',now()); SELECT 1/0; COMMIT;`
	if _, err := psql(t, script); err == nil {
		t.Fatal("expected rollback")
	}
	out, err := psql(t, "SELECT (SELECT count(*) FROM source.projects)::text || ':' || (SELECT count(*) FROM source.outbox)::text;")
	if err != nil || out != "0:0" {
		t.Fatalf("out=%q err=%v", out, err)
	}
}
func TestPostgres_ConcurrentIdempotencySingleWinner(t *testing.T) {
	reset(t)
	const n = 16
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := psql(t, fmt.Sprintf("INSERT INTO source.idempotency(tenant_id,key,command,request_hash,result,completed,created_at) VALUES('t','same','create','hash','{\"winner\":%d}',true,now()) ON CONFLICT DO NOTHING;", i))
			errs <- err
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	out, err := psql(t, "SELECT count(*) FROM source.idempotency WHERE tenant_id='t' AND key='same';")
	if err != nil || out != "1" {
		t.Fatalf("out=%q err=%v", out, err)
	}
}
func TestPostgres_BranchHeadOptimisticLockRejectsStaleUpdate(t *testing.T) {
	reset(t)
	if _, err := psql(t, seedSQL()+" INSERT INTO source.branch_heads(repository_id,name,commit_sha,version) VALUES('r','main',repeat('a',40),1);"); err != nil {
		t.Fatal(err)
	}
	out, err := psql(t, "WITH changed AS (UPDATE source.branch_heads SET commit_sha=repeat('b',40),version=2 WHERE repository_id='r' AND name='main' AND version=1 RETURNING 1) SELECT count(*) FROM changed;")
	if err != nil || out != "1" {
		t.Fatal(out, err)
	}
	out, err = psql(t, "WITH changed AS (UPDATE source.branch_heads SET commit_sha=repeat('c',40),version=2 WHERE repository_id='r' AND name='main' AND version=1 RETURNING 1) SELECT count(*) FROM changed;")
	if err != nil || out != "0" {
		t.Fatal(out, err)
	}
}
func TestPostgres_WebhookInboxDeduplicatesConcurrentDelivery(t *testing.T) {
	reset(t)
	const n = 20
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = psql(t, "INSERT INTO source.webhook_receipts(provider,event_id,body_hash,received_at) VALUES('gitlab','evt','same',now()) ON CONFLICT DO NOTHING;")
		}()
	}
	wg.Wait()
	out, err := psql(t, "SELECT count(*) FROM source.webhook_receipts;")
	if err != nil || out != "1" {
		t.Fatal(out, err)
	}
}
func TestPostgres_WebhookIDCannotBeReusedForDifferentBody(t *testing.T) {
	reset(t)
	if _, err := psql(t, "INSERT INTO source.webhook_receipts(provider,event_id,body_hash,received_at) VALUES('gitlab','evt','one',now());"); err != nil {
		t.Fatal(err)
	}
	out, err := psql(t, "SELECT body_hash FROM source.webhook_receipts WHERE provider='gitlab' AND event_id='evt';")
	if err != nil || out != "one" {
		t.Fatal(out, err)
	}
	if _, err = psql(t, "INSERT INTO source.webhook_receipts(provider,event_id,body_hash,received_at) VALUES('gitlab','evt','two',now());"); err == nil {
		t.Fatal("different body unexpectedly inserted")
	}
}
func TestPostgres_OutOfOrderBranchWriteCannotWinOptimisticRace(t *testing.T) {
	reset(t)
	if _, err := psql(t, seedSQL()+" INSERT INTO source.branch_heads(repository_id,name,commit_sha,observed_at,version) VALUES('r','main',repeat('c',40),now(),3);"); err != nil {
		t.Fatal(err)
	}
	out, err := psql(t, "WITH changed AS (UPDATE source.branch_heads SET commit_sha=repeat('b',40),version=3 WHERE repository_id='r' AND name='main' AND version=2 RETURNING 1) SELECT count(*) FROM changed;")
	if err != nil || out != "0" {
		t.Fatal(out, err)
	}
}
func TestPostgres_AuditRejectsUpdateAndDelete(t *testing.T) {
	reset(t)
	if _, err := psql(t, "INSERT INTO source.audit(id,tenant_id,actor_id,action,resource_type,resource_id,data,created_at) VALUES('a','t','u','x','project','p','{}',now());"); err != nil {
		t.Fatal(err)
	}
	if _, err := psql(t, "UPDATE source.audit SET action='mutated' WHERE id='a';"); err == nil {
		t.Fatal("update accepted")
	}
	if _, err := psql(t, "DELETE FROM source.audit WHERE id='a';"); err == nil {
		t.Fatal("delete accepted")
	}
}
func TestPostgres_RepositoryProviderIdentityRemainsStable(t *testing.T) {
	reset(t)
	if _, err := psql(t, seedSQL()); err != nil {
		t.Fatal(err)
	}
	if _, err := psql(t, "UPDATE source.repositories SET provider_project_id=43 WHERE id='r';"); err == nil {
		t.Fatal("provider identity mutation accepted")
	}
	out, err := psql(t, "SELECT provider_project_id FROM source.repositories WHERE id='r';")
	if err != nil || out != "42" {
		t.Fatal(out, err)
	}
}
func TestPostgres_SerializableSkipLockedRequiresBoundedRetry(t *testing.T) {
	reset(t)
	if _, err := psql(t, "CREATE TABLE source.claims(id int primary key, claimed bool not null default false); INSERT INTO source.claims(id) SELECT generate_series(1,4);"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	worker := func() {
		defer wg.Done()
		var last error
		for attempt := 0; attempt < 5; attempt++ {
			cmd := exec.CommandContext(ctx, "psql", dsn(t), "-X", "--no-psqlrc", "-v", "ON_ERROR_STOP=1", "-At")
			cmd.Stdin = strings.NewReader("BEGIN ISOLATION LEVEL SERIALIZABLE; WITH picked AS (SELECT id FROM source.claims WHERE NOT claimed ORDER BY id FOR UPDATE SKIP LOCKED LIMIT 2) UPDATE source.claims c SET claimed=true FROM picked WHERE c.id=picked.id; SELECT pg_sleep(0.05); COMMIT;")
			raw, err := cmd.CombinedOutput()
			if err == nil {
				errs <- nil
				return
			}
			last = fmt.Errorf("%w: %s", err, raw)
			if !strings.Contains(string(raw), "could not serialize") && !strings.Contains(string(raw), "deadlock") {
				break
			}
			time.Sleep(time.Duration(attempt+1) * 20 * time.Millisecond)
		}
		errs <- last
	}
	wg.Add(2)
	go worker()
	go worker()
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	out, err := psql(t, "SELECT count(*) FROM source.claims WHERE claimed;")
	if err != nil || out != "4" {
		t.Fatal(out, err)
	}
}
