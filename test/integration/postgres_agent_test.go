//go:build postgres_integration

package integration_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/keir-research/ai-native-paas/internal/agent/application"
	"github.com/keir-research/ai-native-paas/internal/agent/domain"
	agentpostgres "github.com/keir-research/ai-native-paas/internal/agent/postgres"
	"github.com/keir-research/ai-native-paas/internal/agent/testkit"
	agentv1 "github.com/keir-research/ai-native-paas/pkg/contracts/agent/v1"
	buildv1 "github.com/keir-research/ai-native-paas/pkg/contracts/build/v1"
	runtimev1 "github.com/keir-research/ai-native-paas/pkg/contracts/runtime/v1"
)

func migratedAgentStore(t *testing.T) (*sql.DB, *agentpostgres.Store) {
	t.Helper()
	db := openPostgres(t)
	if _, err := db.Exec(`DROP SCHEMA IF EXISTS agent CASCADE`); err != nil {
		t.Fatal(err)
	}
	store, err := agentpostgres.NewStore(db)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	return db, store
}

type pgAgentFixture struct {
	db      *sql.DB
	store   *agentpostgres.Store
	svc     *application.Service
	clock   *testkit.Clock
	builds  *testkit.Builds
	runtime *testkit.Runtime
}

func newPGAgentFixture(t *testing.T) *pgAgentFixture {
	t.Helper()
	db, store := migratedAgentStore(t)
	clock := &testkit.Clock{T: time.Date(2026, 7, 13, 9, 0, 0, 0, time.UTC)}
	ids := &testkit.IDs{}
	builds := testkit.NewBuilds()
	runtime := testkit.NewRuntime()
	svc := &application.Service{Store: store, Clock: clock, IDs: ids, Source: testkit.NewSource(), Builds: builds, Runtime: runtime, Attachments: testkit.NewAttachments(), Commerce: &testkit.Commerce{Allowed: true}, Operations: testkit.NewOperations(), Logs: &testkit.Logs{}, Usage: &testkit.Usage{}}
	scopes := []string{}
	for _, tool := range agentv1.ToolCatalog() {
		scopes = append(scopes, string(agentv1.ScopeForTool(tool)))
	}
	if _, err := svc.RegisterPrincipal(context.Background(), application.RegisterPrincipalCommand{ID: "agent-1", TenantID: "tenant-1", OnBehalfOfUserID: "user-1", Scopes: scopes, CredentialExpiresAt: clock.T.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.StartTask(context.Background(), application.StartTaskCommand{ID: "task-1", TenantID: "tenant-1", AgentID: "agent-1", OnBehalfOfUserID: "user-1", CorrelationID: "corr-1", BudgetPolicy: agentv1.BudgetPolicy{MaxBuildCount: 3, MaxBuildMinutes: 3, MaxDeployCount: 3, RepairThreshold: 3}}); err != nil {
		t.Fatal(err)
	}
	return &pgAgentFixture{db: db, store: store, svc: svc, clock: clock, builds: builds, runtime: runtime}
}
func pgInvocation(tool agentv1.Tool, args any, key string) agentv1.InvocationRequest {
	raw, _ := json.Marshal(args)
	return agentv1.InvocationRequest{APIVersion: agentv1.APIVersion, SemanticsVersion: agentv1.SemanticsVersion, TenantID: "tenant-1", AgentID: "agent-1", TaskID: "task-1", Tool: tool, Arguments: raw, IdempotencyKey: key, CorrelationID: "corr-1"}
}
func pgBuildArgs() application.RequestBuildArguments {
	var a application.RequestBuildArguments
	a.Revision.ProjectID = "project-1"
	a.Revision.RepositoryID = "repo-1"
	a.Revision.Branch = "main"
	a.Revision.CommitSHA = "abcdef0123456789"
	a.EstimatedMinutes = 1
	return a
}
func pgDeployArgs() application.DeployArguments {
	return application.DeployArguments{BuildID: "build-1", ApplicationID: "app-1", EnvironmentID: "env-production", EnvironmentName: "production", ExpectedEnvironmentRevision: 1, Configuration: runtimev1.ReleaseConfig{Region: "eu1", Isolation: runtimev1.IsolationSandboxed, Unit: "u1", Processes: map[string]runtimev1.ProcessSpec{"web": {Port: 8080, MinReplicas: 1, MaxReplicas: 1}}, RolloutTimeoutSeconds: 300}}
}
func seedPGBuild(f *pgAgentFixture) {
	a := buildv1.ArtifactRef{ArtifactID: "artifact-1", Repository: "registry.invalid/app", Digest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", MediaType: "application/vnd.oci.image.manifest.v1+json"}
	f.builds.ByID["build-1"] = buildv1.BuildView{BuildID: "build-1", TenantID: "tenant-1", State: buildv1.BuildSucceeded, Artifact: &a}
}

func TestPostgres_AgentMigrationsCleanInstallAndUpgrade(t *testing.T) {
	db, store := migratedAgentStore(t)
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM agent.schema_migrations`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 3 {
		t.Fatalf("migrations=%d", count)
	}
	for _, table := range []string{"principals", "tasks", "invocations", "approval_requests", "approval_grants", "outbox", "audit"} {
		var ok bool
		if err := db.QueryRow(`SELECT EXISTS(SELECT 1 FROM information_schema.tables WHERE table_schema='agent' AND table_name=$1)`, table).Scan(&ok); err != nil || !ok {
			t.Fatalf("table %s ok=%v err=%v", table, ok, err)
		}
	}
}
func TestPostgres_AgentAggregateAndOutboxAreAtomic(t *testing.T) {
	db, store := migratedAgentStore(t)
	now := time.Now().UTC()
	err := store.Transact(context.Background(), func(tx application.Tx) error {
		if err := tx.InsertTask(domain.AgentTask{ID: "task-x", TenantID: "tenant-1", AgentID: "agent-1", OnBehalfOfUserID: "user-1", CorrelationID: "corr", State: domain.TaskActive, BudgetPolicy: agentv1.BudgetPolicy{RepairThreshold: 1}, Version: 1, CreatedAt: now, UpdatedAt: now}); err != nil {
			return err
		}
		if err := tx.AppendOutbox(domain.OutboxRecord{ID: "event-x", TenantID: "tenant-1", Topic: "x", AggregateID: "task-x", Payload: json.RawMessage(`{"ok":true}`), CreatedAt: now}); err != nil {
			return err
		}
		return fmt.Errorf("force rollback")
	})
	if err == nil {
		t.Fatal("expected rollback")
	}
	for _, q := range []string{`SELECT count(*) FROM agent.tasks WHERE id='task-x'`, `SELECT count(*) FROM agent.outbox WHERE id='event-x'`} {
		var n int
		if err := db.QueryRow(q).Scan(&n); err != nil || n != 0 {
			t.Fatalf("n=%d err=%v", n, err)
		}
	}
}
func TestPostgres_AgentConcurrentIdempotencyUsesSingleSideEffect(t *testing.T) {
	f := newPGAgentFixture(t)
	req := pgInvocation(agentv1.ToolCreateProject, application.CreateProjectArguments{Name: "booking"}, "same")
	var wg sync.WaitGroup
	ids := make(chan string, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := f.svc.Invoke(context.Background(), req)
			if err == nil && r.Result != nil {
				ids <- r.Result.ID
			}
		}()
	}
	wg.Wait()
	close(ids)
	first := ""
	for id := range ids {
		if first == "" {
			first = id
		}
		if id != first {
			t.Fatalf("ids %s %s", first, id)
		}
	}
	var n int
	if err := f.db.QueryRow(`SELECT count(*) FROM agent.invocations`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("n=%d err=%v", n, err)
	}
}
func TestPostgres_AgentConcurrentBudgetCannotOversubscribe(t *testing.T) {
	f := newPGAgentFixture(t)
	var wg sync.WaitGroup
	success := 0
	failures := map[string]int{}
	var mu sync.Mutex
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, invokeErr := f.svc.Invoke(context.Background(), pgInvocation(agentv1.ToolRequestBuild, pgBuildArgs(), fmt.Sprintf("b-%d", i)))
			mu.Lock()
			if invokeErr == nil {
				success++
			} else {
				failures[invokeErr.Error()]++
			}
			mu.Unlock()
		}(i)
	}
	wg.Wait()
	if success != 3 {
		t.Fatalf("success=%d failures=%v", success, failures)
	}
	v, err := f.svc.GetTask(context.Background(), "tenant-1", "task-1")
	if err != nil || v.BudgetUsage.BuildCount != 3 {
		t.Fatalf("view=%+v err=%v", v, err)
	}
}
func TestPostgres_AgentApprovalGrantIsSingleUseUnderConcurrency(t *testing.T) {
	f := newPGAgentFixture(t)
	seedPGBuild(f)
	a := pgDeployArgs()
	payload, _ := json.Marshal(a)
	r, err := f.svc.Invoke(context.Background(), pgInvocation(agentv1.ToolRequestApproval, application.RequestApprovalArguments{Action: agentv1.ApprovalDeployProduction, Resource: agentv1.ApprovalResource{Type: "environment", ID: a.EnvironmentID}, Payload: payload, TTLSeconds: 600}, "approval"))
	if err != nil {
		t.Fatal(err)
	}
	var view agentv1.ApprovalRequestView
	_ = json.Unmarshal(r.Result.Data, &view)
	g, err := f.svc.GrantApprovalForTenant(context.Background(), "tenant-1", view.ApprovalRequestID, "approver-1", "user")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	success := 0
	var mu sync.Mutex
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			req := pgInvocation(agentv1.ToolDeploy, a, fmt.Sprintf("d-%d", i))
			req.ApprovalGrantID = g.ID
			if _, err := f.svc.Invoke(context.Background(), req); err == nil {
				mu.Lock()
				success++
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()
	if success != 1 {
		t.Fatalf("success=%d", success)
	}
}
func TestPostgres_AgentOptimisticLockPreventsLostUpdate(t *testing.T) {
	f := newPGAgentFixture(t)
	var task domain.AgentTask
	if err := f.store.Transact(context.Background(), func(tx application.Tx) error { task, _ = tx.GetTask("task-1"); return nil }); err != nil {
		t.Fatal(err)
	}
	copy := task
	task.State = domain.TaskPaused
	task.Version++
	if err := f.store.Transact(context.Background(), func(tx application.Tx) error { return tx.UpdateTask(task, 1) }); err != nil {
		t.Fatal(err)
	}
	copy.State = domain.TaskCanceled
	copy.Version++
	if err := f.store.Transact(context.Background(), func(tx application.Tx) error { return tx.UpdateTask(copy, 1) }); err == nil {
		t.Fatal("stale update accepted")
	}
}
func TestPostgres_AgentAuditAndOutboxAreAppendOnly(t *testing.T) {
	f := newPGAgentFixture(t)
	if _, err := f.svc.Invoke(context.Background(), pgInvocation(agentv1.ToolCreateProject, application.CreateProjectArguments{Name: "booking"}, "k")); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{`UPDATE agent.audit SET outcome='FORGED'`, `DELETE FROM agent.outbox`} {
		if _, err := f.db.Exec(q); err == nil {
			t.Fatalf("mutation accepted: %s", q)
		}
	}
}
func TestPostgres_AgentInvocationIdentityIsImmutable(t *testing.T) {
	f := newPGAgentFixture(t)
	if _, err := f.svc.Invoke(context.Background(), pgInvocation(agentv1.ToolCreateProject, application.CreateProjectArguments{Name: "booking"}, "k")); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(`UPDATE agent.invocations SET tool='platform_get_project'`); err == nil {
		t.Fatal("identity mutation accepted")
	}
}
func TestPostgres_AgentPayloadConsistencyRejectsColumnDrift(t *testing.T) {
	f := newPGAgentFixture(t)
	if _, err := f.svc.Invoke(context.Background(), pgInvocation(agentv1.ToolCreateProject, application.CreateProjectArguments{Name: "booking"}, "k")); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(`UPDATE agent.invocations SET state='FORGED'`); err == nil {
		t.Fatal("payload drift accepted")
	}
}
func TestPostgres_AgentSecretNeverPersistsInControlPlane(t *testing.T) {
	f := newPGAgentFixture(t)
	secret := "super-secret-never-store"
	if _, err := f.svc.Invoke(context.Background(), pgInvocation(agentv1.ToolSetSecret, application.SetSecretArguments{ApplicationID: "app-1", EnvironmentID: "env-1", Name: "TOKEN", Value: secret}, "s")); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"invocations", "audit", "outbox"} {
		var n int
		q := fmt.Sprintf(`SELECT count(*) FROM agent.%s WHERE payload::text LIKE $1`, table)
		if err := f.db.QueryRow(q, "%"+secret+"%").Scan(&n); err != nil || n != 0 {
			t.Fatalf("table=%s n=%d err=%v", table, n, err)
		}
	}
}
func TestPostgres_AgentNoCrossDomainForeignKeys(t *testing.T) {
	db, _ := migratedAgentStore(t)
	rows, err := db.Query(`SELECT ccu.table_schema FROM information_schema.table_constraints tc JOIN information_schema.constraint_column_usage ccu ON ccu.constraint_name=tc.constraint_name AND ccu.constraint_schema=tc.constraint_schema WHERE tc.constraint_type='FOREIGN KEY' AND tc.table_schema='agent'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var schema string
		if err := rows.Scan(&schema); err != nil {
			t.Fatal(err)
		}
		if schema != "agent" {
			t.Fatalf("cross-domain FK to %s", schema)
		}
	}
}
func TestPostgres_AgentMigrationsContainNoSecretValueColumns(t *testing.T) {
	db, _ := migratedAgentStore(t)
	rows, err := db.Query(`SELECT column_name FROM information_schema.columns WHERE table_schema='agent'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		_ = rows.Scan(&name)
		lower := strings.ToLower(name)
		if strings.Contains(lower, "password") || strings.Contains(lower, "secret_value") || strings.Contains(lower, "token_value") {
			t.Fatalf("forbidden column %s", name)
		}
	}
}
