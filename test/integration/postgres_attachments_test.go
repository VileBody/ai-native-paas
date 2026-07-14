//go:build postgres_integration

package integration_test

import (
	"context"
	"crypto/ed25519"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/keir-research/ai-native-paas/internal/attachments/application"
	"github.com/keir-research/ai-native-paas/internal/attachments/domain"
	attachmentspostgres "github.com/keir-research/ai-native-paas/internal/attachments/postgres"
	"github.com/keir-research/ai-native-paas/internal/attachments/recipe"
	"github.com/keir-research/ai-native-paas/internal/attachments/testkit"
	attachmentsv1 "github.com/keir-research/ai-native-paas/pkg/contracts/attachments/v1"
	attachmentsv2 "github.com/keir-research/ai-native-paas/pkg/contracts/attachments/v2"
	recipesv1 "github.com/keir-research/ai-native-paas/pkg/contracts/recipes/v1"
)

func migratedAttachmentsStore(t *testing.T) (*sql.DB, *attachmentspostgres.Store) {
	t.Helper()
	db := openPostgres(t)
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `DROP SCHEMA IF EXISTS attachments CASCADE`); err != nil {
		t.Fatalf("reset attachments schema: %v", err)
	}
	store, err := attachmentspostgres.NewStore(db)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("migrate attachments: %v", err)
	}
	return db, store
}

func pgAttachmentPlan(t *testing.T, id string) domain.ServicePlan {
	t.Helper()
	plan, err := domain.NewServicePlan(id, 1, attachmentsv1.ServicePostgreSQL, "cozystack", "small", "map-v1", false, []string{"read", "write"}, true, true, time.Date(2026, 7, 13, 10, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func TestPostgres_AttachmentsMigrationsCleanInstallAndUpgrade(t *testing.T) {
	db, store := migratedAttachmentsStore(t)
	ctx := context.Background()
	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("idempotent migrate: %v", err)
	}
	var count int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM attachments.schema_migrations`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 6 {
		t.Fatalf("migration count=%d want=6", count)
	}
	for _, table := range []string{"secret_sets", "secrets", "service_plans", "service_instances", "service_bindings", "domain_claims", "snapshots", "environment_inputs_snapshots", "recipe_versions", "idempotency", "outbox", "audit"} {
		var exists bool
		if err := db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_schema='attachments' AND table_name=$1)`, table).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		if !exists {
			t.Fatalf("missing attachments.%s", table)
		}
	}
}

func TestInputsSnapshot_IsImmutableAndContainsReferencesOnly(t *testing.T) {
	db, store := migratedAttachmentsStore(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 14, 13, 0, 0, 0, time.UTC)
	snapshot := attachmentsv2.EnvironmentInputsSnapshot{
		SnapshotID: "inputs-1", ProjectID: "project-1", Environment: "production", CreatedAt: now,
		SecretVersions:     []attachmentsv2.SecretVersionRef{{SecretID: "secret-1", Name: "API_TOKEN", Version: 1, ValueHash: "sha256:" + strings.Repeat("a", 64)}},
		CredentialBindings: []attachmentsv2.CredentialBindingRef{{BindingID: "credential-binding-1", Provider: "openbao", CredentialID: "credential-1", Audience: "runtime", ExpiresAt: now.Add(time.Hour)}},
		Resources:          []attachmentsv2.ManagedResourceRef{{ResourceID: "resource-1", Provider: "cozystack", Kind: "postgresql", ExternalIdentity: "postgresql/project-1/main", LifecyclePolicy: "retain"}},
		Capabilities:       []attachmentsv2.CapabilityBindingRef{{BindingID: "capability-1", Kind: "llm", Endpoint: "https://gateway.example/v1", TokenRef: "openbao/project-1/llm", BudgetID: "budget-1"}},
		Recipes:            []attachmentsv2.RecipeLock{{RecipeID: "postgresql", Version: "1.0.0", ContentDigest: "sha256:" + strings.Repeat("b", 64), SignatureDigest: "sha256:" + strings.Repeat("c", 64)}},
	}
	snapshot.Digest, _ = snapshot.Fingerprint()
	stored, err := store.PublishEnvironmentInputsSnapshot(ctx, snapshot)
	if err != nil || stored.Digest != snapshot.Digest {
		t.Fatalf("stored=%+v err=%v", stored, err)
	}
	if replay, replayErr := store.PublishEnvironmentInputsSnapshot(ctx, snapshot); replayErr != nil || replay.Digest != snapshot.Digest {
		t.Fatalf("exact retry=%+v err=%v", replay, replayErr)
	}

	mutated := snapshot
	mutated.SecretVersions = append([]attachmentsv2.SecretVersionRef(nil), snapshot.SecretVersions...)
	mutated.SecretVersions[0].Version = 2
	mutated.Digest, _ = mutated.Fingerprint()
	if _, err = store.PublishEnvironmentInputsSnapshot(ctx, mutated); !attachmentErrorCode(err, domain.CodeConflict) {
		t.Fatalf("same-ID mutation was accepted: %v", err)
	}
	if _, err = db.ExecContext(ctx, `UPDATE attachments.environment_inputs_snapshots SET digest=$1 WHERE id=$2`, "sha256:"+strings.Repeat("d", 64), snapshot.SnapshotID); err == nil {
		t.Fatal("direct snapshot update was accepted")
	}
	if _, err = db.ExecContext(ctx, `DELETE FROM attachments.environment_inputs_snapshots WHERE id=$1`, snapshot.SnapshotID); err == nil {
		t.Fatal("direct snapshot deletion was accepted")
	}

	raw, _ := json.Marshal(snapshot)
	withPlaintext := strings.TrimSuffix(string(raw), "}") + `,"value":"plaintext-secret-sentinel"}`
	if _, err = attachmentsv2.DecodeEnvironmentInputsSnapshot([]byte(withPlaintext)); err == nil {
		t.Fatal("plaintext value field was accepted by strict snapshot decoder")
	}
	var persisted string
	if err = db.QueryRowContext(ctx, `SELECT payload::text FROM attachments.environment_inputs_snapshots WHERE id=$1`, snapshot.SnapshotID).Scan(&persisted); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(persisted, "plaintext-secret-sentinel") || strings.Contains(persisted, `"value"`) || strings.Contains(persisted, `"password"`) {
		t.Fatalf("snapshot payload contains plaintext-shaped data: %s", persisted)
	}
}

func TestRecipe_ActivatedVersionIsImmutableAndSigned(t *testing.T) {
	db, store := migratedAttachmentsStore(t)
	ctx := context.Background()
	publicKey, privateKey, err := ed25519.GenerateKey(strings.NewReader(strings.Repeat("r", ed25519.SeedSize)))
	if err != nil {
		t.Fatal(err)
	}
	unsigned := recipesv1.RecipeVersion{
		RecipeID: "postgresql", Version: "1.0.0", Kind: "stateful-service", Driver: "cozystack",
		SchemaDigest: "sha256:" + strings.Repeat("a", 64), Stateful: true,
		Capabilities: recipesv1.LifecycleCapabilities{Plan: true, Apply: true, Discover: true, Update: true, Retain: true, Purge: true, Backup: true, Restore: true},
		Artifacts:    []recipesv1.ArtifactPin{{Name: "module", Kind: "opentofu", Reference: "registry.example/modules/postgresql", Digest: "sha256:" + strings.Repeat("b", 64)}},
		Permissions:  []recipesv1.ResourcePermission{{APIGroup: "apps", Kind: "StatefulSet", Scope: recipesv1.PermissionNamespaced}},
		Procedures:   recipesv1.LifecycleProcedures{HealthCheck: "ready-v1", Backup: "backup-v1", Restore: "restore-v1", Upgrade: "upgrade-v1", Removal: "remove-v1", Retention: "retain-until-approved"},
	}
	signed, err := recipe.Sign(unsigned, "beta-recipe-key", privateKey)
	if err != nil || recipe.Verify(signed, publicKey) != nil {
		t.Fatalf("sign err=%v verify=%v", err, recipe.Verify(signed, publicKey))
	}
	registry := recipe.Registry{Store: store, Keys: map[string]ed25519.PublicKey{"beta-recipe-key": publicKey}}
	activated, err := registry.Activate(ctx, signed)
	if err != nil || activated.ContentDigest != signed.ContentDigest {
		t.Fatalf("activated=%+v err=%v", activated, err)
	}
	if replay, replayErr := registry.Activate(ctx, signed); replayErr != nil || replay.ContentDigest != signed.ContentDigest {
		t.Fatalf("replay=%+v err=%v", replay, replayErr)
	}

	modified := unsigned
	modified.Artifacts = append([]recipesv1.ArtifactPin(nil), unsigned.Artifacts...)
	modified.Artifacts[0].Digest = "sha256:" + strings.Repeat("c", 64)
	modified, err = recipe.Sign(modified, "beta-recipe-key", privateKey)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = registry.Activate(ctx, modified); !attachmentErrorCode(err, domain.CodeConflict) {
		t.Fatalf("activated recipe version was mutated: %v", err)
	}
	if _, err = db.ExecContext(ctx, `UPDATE attachments.recipe_versions SET content_digest=$1 WHERE recipe_id=$2 AND version=$3`, "sha256:"+strings.Repeat("d", 64), signed.RecipeID, signed.Version); err == nil {
		t.Fatal("direct active recipe update was accepted")
	}
	if _, err = db.ExecContext(ctx, `DELETE FROM attachments.recipe_versions WHERE recipe_id=$1 AND version=$2`, signed.RecipeID, signed.Version); err == nil {
		t.Fatal("direct active recipe deletion was accepted")
	}
	tampered := signed
	tampered.Driver = "untrusted-driver"
	if recipe.Verify(tampered, publicKey) == nil {
		t.Fatal("tampered canonical recipe content retained a valid signature")
	}
}

func attachmentErrorCode(err error, code domain.Code) bool {
	var typed *domain.Error
	return errors.As(err, &typed) && typed.Code == code
}

func TestSecret_ValueAbsentFromControlPlanePersistenceAndEvents(t *testing.T) {
	db, store := migratedAttachmentsStore(t)
	ctx := context.Background()
	clock := testkit.NewClock()
	vault := testkit.NewVault()
	runtime := testkit.NewRuntime()
	logger := &testkit.Logger{}
	service := &application.Service{
		Store: store,
		Environments: &testkit.Environments{Values: map[string]application.EnvironmentRef{
			"env-a": {TenantID: "tenant-a", ApplicationID: "app-a", EnvironmentID: "env-a", Name: "production", Ready: true},
		}},
		Secrets: vault, Runtime: runtime, Logger: logger, Clock: clock, IDs: &application.SequentialIDs{},
	}
	const (
		first  = "a5-3-first-plaintext-secret-sentinel"
		second = "a5-3-rotated-plaintext-secret-sentinel"
	)

	metadata, snapshot, err := service.SetSecret(ctx, application.SetSecretRequest{
		TenantID: "tenant-a", ApplicationID: "app-a", EnvironmentID: "env-a", Name: "API_TOKEN",
		Scope: attachmentsv1.SecretScopeRuntime, Phase: attachmentsv1.SecretPhaseRuntime,
		Value: []byte(first), ActorID: "user-a", IdempotencyKey: "set-first",
	})
	if err != nil || metadata.Name != "API_TOKEN" || snapshot.SnapshotID == "" || !vault.Contains(first) {
		t.Fatalf("set metadata=%#v snapshot=%#v vault=%t err=%v", metadata, snapshot, vault.Contains(first), err)
	}
	var secretID string
	if err := store.Transact(ctx, func(tx application.Tx) error {
		set, ok := tx.FindSecretSet("tenant-a", "env-a")
		if !ok {
			return errors.New("secret set missing after write")
		}
		stored, ok := tx.FindSecret(set.ID, "API_TOKEN", attachmentsv1.SecretScopeRuntime)
		if !ok {
			return errors.New("secret metadata missing after write")
		}
		secretID = stored.ID
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	assertAttachmentsControlPlaneExcludes(t, db, logger, runtime, first, second)

	rotated, rotatedSnapshot, err := service.SetSecret(ctx, application.SetSecretRequest{
		TenantID: "tenant-a", ApplicationID: "app-a", EnvironmentID: "env-a", Name: "API_TOKEN",
		Scope: attachmentsv1.SecretScopeRuntime, Phase: attachmentsv1.SecretPhaseRuntime,
		Value: []byte(second), ActorID: "user-a", IdempotencyKey: "set-rotated",
	})
	if err != nil || rotated.Version <= metadata.Version || rotatedSnapshot.Version <= snapshot.Version || !vault.Contains(second) {
		t.Fatalf("rotate metadata=%#v snapshot=%#v vault=%t err=%v", rotated, rotatedSnapshot, vault.Contains(second), err)
	}
	assertAttachmentsControlPlaneExcludes(t, db, logger, runtime, first, second)

	deletedSnapshot, err := service.DeleteSecret(ctx, application.DeleteSecretRequest{
		TenantID: "tenant-a", EnvironmentID: "env-a", SecretID: secretID,
		ActorID: "user-a", IdempotencyKey: "delete-secret",
	})
	if err != nil || deletedSnapshot.Version <= rotatedSnapshot.Version || vault.Contains(first) || vault.Contains(second) {
		t.Fatalf("delete snapshot=%#v first=%t second=%t err=%v", deletedSnapshot, vault.Contains(first), vault.Contains(second), err)
	}
	assertAttachmentsControlPlaneExcludes(t, db, logger, runtime, first, second)
}

func assertAttachmentsControlPlaneExcludes(t *testing.T, db *sql.DB, logger *testkit.Logger, runtime *testkit.Runtime, sentinels ...string) {
	t.Helper()
	rows, err := db.Query(`SELECT tablename FROM pg_catalog.pg_tables WHERE schemaname='attachments' ORDER BY tablename`)
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var table string
		if err := rows.Scan(&table); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		tables = append(tables, table)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	for _, table := range tables {
		identifier := `"` + strings.ReplaceAll(table, `"`, `""`) + `"`
		values, err := db.Query(`SELECT row_to_json(stored_row)::text FROM attachments.` + identifier + ` AS stored_row`)
		if err != nil {
			t.Fatalf("inspect attachments.%s: %v", table, err)
		}
		for values.Next() {
			var value string
			if err := values.Scan(&value); err != nil {
				values.Close()
				t.Fatal(err)
			}
			for _, sentinel := range sentinels {
				if strings.Contains(value, sentinel) {
					values.Close()
					t.Fatalf("attachments.%s persisted plaintext sentinel", table)
				}
			}
		}
		if err := values.Close(); err != nil {
			t.Fatal(err)
		}
		if err := values.Err(); err != nil {
			t.Fatal(err)
		}
	}
	for _, sentinel := range sentinels {
		if logger.Contains(sentinel) {
			t.Fatalf("structured logs contain plaintext sentinel")
		}
	}
	rawSnapshots, err := json.Marshal(runtime.Snapshots)
	if err != nil {
		t.Fatal(err)
	}
	for _, sentinel := range sentinels {
		if strings.Contains(string(rawSnapshots), sentinel) {
			t.Fatalf("runtime snapshot contains plaintext sentinel")
		}
	}
}

func TestPostgres_AttachmentMutationAndOutboxCommitAtomically(t *testing.T) {
	db, store := migratedAttachmentsStore(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 13, 10, 0, 0, 0, time.UTC)
	set := domain.SecretSet{ID: "secset-atomic", TenantID: "tenant-a", ApplicationID: "app-a", EnvironmentID: "env-a", ProviderPath: "tenants/tenant-a/apps/app-a/env-a", Version: 1, CreatedAt: now, UpdatedAt: now}
	event := domain.OutboxRecord{ID: "evt-atomic", TenantID: "tenant-a", Topic: "attachments.secret-set.created.v1", AggregateID: set.ID, Payload: []byte(`{"secret_set_id":"secset-atomic"}`), CreatedAt: now}
	sentinel := errors.New("force rollback")
	if err := store.Transact(ctx, func(tx application.Tx) error {
		if err := tx.PutSecretSet(set, 0); err != nil {
			return err
		}
		if err := tx.AppendOutbox(event); err != nil {
			return err
		}
		return sentinel
	}); !errors.Is(err, sentinel) {
		t.Fatalf("rollback error=%v", err)
	}
	for _, table := range []string{"secret_sets", "outbox"} {
		var count int
		if err := db.QueryRowContext(ctx, `SELECT count(*) FROM attachments.`+table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("%s count after rollback=%d", table, count)
		}
	}
	if err := store.Transact(ctx, func(tx application.Tx) error {
		if err := tx.PutSecretSet(set, 0); err != nil {
			return err
		}
		return tx.AppendOutbox(event)
	}); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"secret_sets", "outbox"} {
		var count int
		if err := db.QueryRowContext(ctx, `SELECT count(*) FROM attachments.`+table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("%s count after commit=%d want=1", table, count)
		}
	}
}

func TestPostgres_AttachmentsConcurrentIdempotencyUsesSingleWinner(t *testing.T) {
	db, store := migratedAttachmentsStore(t)
	ctx := context.Background()
	clock := testkit.NewClock()
	provider := &testkit.Provider{EnsureResult: application.ProviderInstanceResult{ProviderID: "provider-one", Ready: true}}
	service := &application.Service{Store: store, Provider: provider, Commerce: &testkit.Commerce{Allowed: true}, Usage: &testkit.Usage{}, Clock: clock, IDs: &application.SequentialIDs{}}
	if err := service.RegisterServicePlan(ctx, pgAttachmentPlan(t, "pg-small"), "admin"); err != nil {
		t.Fatal(err)
	}

	const workers = 16
	var group sync.WaitGroup
	ids := make(chan string, workers)
	errs := make(chan error, workers)
	group.Add(workers)
	for i := 0; i < workers; i++ {
		go func() {
			defer group.Done()
			instance, err := service.ProvisionService(context.Background(), application.ProvisionServiceRequest{TenantID: "tenant-a", Name: "primary", PlanID: "pg-small", IdempotencyKey: "same-command", ActorID: "user-a"})
			if err != nil {
				errs <- err
				return
			}
			ids <- instance.ID
		}()
	}
	group.Wait()
	close(ids)
	close(errs)
	for err := range errs {
		t.Errorf("provision: %v", err)
	}
	winners := map[string]bool{}
	for id := range ids {
		winners[id] = true
	}
	if len(winners) != 1 {
		t.Fatalf("winners=%v want one", winners)
	}
	var instances, idem int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM attachments.service_instances`).Scan(&instances); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM attachments.idempotency WHERE scope='service.provision'`).Scan(&idem); err != nil {
		t.Fatal(err)
	}
	if instances != 1 || idem != 1 {
		t.Fatalf("instances=%d idempotency=%d", instances, idem)
	}
	if provider.EnsureCalls != 1 {
		t.Fatalf("provider calls=%d want=1", provider.EnsureCalls)
	}
}

func TestPostgres_AttachmentsOptimisticLockPreventsLostUpdate(t *testing.T) {
	_, store := migratedAttachmentsStore(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 13, 10, 0, 0, 0, time.UTC)
	instance := domain.ServiceInstance{ID: "svc-lock", TenantID: "tenant-a", Name: "primary", PlanID: "pg-small", PlanVersion: 1, Type: attachmentsv1.ServicePostgreSQL, State: attachmentsv1.ServiceRequested, ProviderOperationKey: "op-lock", Version: 1, CreatedAt: now, UpdatedAt: now}
	if err := store.Transact(ctx, func(tx application.Tx) error {
		if err := tx.PutPlan(pgAttachmentPlan(t, instance.PlanID)); err != nil {
			return err
		}
		return tx.PutInstance(instance, 0)
	}); err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func(i int) {
			<-start
			results <- store.Transact(context.Background(), func(tx application.Tx) error {
				v, ok := tx.GetInstance(instance.ID)
				if !ok {
					return errors.New("missing")
				}
				time.Sleep(20 * time.Millisecond)
				v.Version++
				v.UpdatedAt = now.Add(time.Duration(i+1) * time.Minute)
				v.State = attachmentsv1.ServiceProvisioning
				return tx.PutInstance(v, 1)
			})
		}(i)
	}
	close(start)
	var success, failure int
	for i := 0; i < 2; i++ {
		if err := <-results; err == nil {
			success++
		} else {
			failure++
		}
	}
	if success != 1 || failure != 1 {
		t.Fatalf("success=%d failure=%d", success, failure)
	}
}

func TestPostgres_AttachmentsProviderIdentityIsImmutable(t *testing.T) {
	db, store := migratedAttachmentsStore(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 13, 10, 0, 0, 0, time.UTC)
	instance := domain.ServiceInstance{ID: "svc-provider", TenantID: "tenant-a", Name: "primary", PlanID: "pg", PlanVersion: 1, Type: attachmentsv1.ServicePostgreSQL, State: attachmentsv1.ServiceReady, ProviderOperationKey: "op-provider", ProviderID: "provider-a", Version: 1, CreatedAt: now, UpdatedAt: now}
	if err := store.Transact(ctx, func(tx application.Tx) error {
		if err := tx.PutPlan(pgAttachmentPlan(t, instance.PlanID)); err != nil {
			return err
		}
		return tx.PutInstance(instance, 0)
	}); err != nil {
		t.Fatal(err)
	}
	_, err := db.ExecContext(ctx, `UPDATE attachments.service_instances SET provider_id='provider-b' WHERE id='svc-provider'`)
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "provider identity") {
		t.Fatalf("provider mutation err=%v", err)
	}
}

func TestPostgres_AttachmentSnapshotIsImmutable(t *testing.T) {
	db, store := migratedAttachmentsStore(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 13, 10, 0, 0, 0, time.UTC)
	snap := domain.AttachmentSnapshot{Value: attachmentsv1.AttachmentSnapshot{SnapshotID: "ats-immutable", TenantID: "tenant-a", ApplicationID: "app-a", EnvironmentID: "env-a", Version: 1, SecretSetRef: "secset-a:v1", CreatedAt: now}, ContentHash: domain.Hash("snapshot")}
	if err := store.Transact(ctx, func(tx application.Tx) error { return tx.PutSnapshot(snap) }); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE attachments.snapshots SET content_hash=$1 WHERE id=$2`, domain.Hash("changed"), snap.Value.SnapshotID); err == nil {
		t.Fatal("snapshot update succeeded")
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM attachments.snapshots WHERE id=$1`, snap.Value.SnapshotID); err == nil {
		t.Fatal("snapshot delete succeeded")
	}
}

func TestPostgres_AttachmentsAuditRejectsMutation(t *testing.T) {
	db, store := migratedAttachmentsStore(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 13, 10, 0, 0, 0, time.UTC)
	audit := domain.AuditRecord{ID: "aud-1", TenantID: "tenant-a", ActorID: "user-a", Action: "attachments.test", ResourceType: "attachment", ResourceID: "x", Data: []byte(`{"safe":true}`), CreatedAt: now}
	if err := store.Transact(ctx, func(tx application.Tx) error { return tx.AppendAudit(audit) }); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE attachments.audit SET action='tampered' WHERE id='aud-1'`); err == nil {
		t.Fatal("audit update succeeded")
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM attachments.audit WHERE id='aud-1'`); err == nil {
		t.Fatal("audit delete succeeded")
	}
}

func TestPostgres_AttachmentsPayloadConsistencyRejectsColumnDrift(t *testing.T) {
	db, _ := migratedAttachmentsStore(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 13, 10, 0, 0, 0, time.UTC)
	payload, _ := json.Marshal(domain.SecretSet{ID: "set-drift", TenantID: "tenant-a", ApplicationID: "app-a", EnvironmentID: "env-a", ProviderPath: "tenants/tenant-a/apps/app-a/env-a", Version: 1, CreatedAt: now, UpdatedAt: now})
	_, err := db.ExecContext(ctx, `INSERT INTO attachments.secret_sets(id,tenant_id,application_id,environment_id,provider_path,version,payload,created_at,updated_at) VALUES('set-drift','tenant-b','app-a','env-a','tenants/tenant-a/apps/app-a/env-a',1,$1,$2,$2)`, payload, now)
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "payload drift") {
		t.Fatalf("drift err=%v", err)
	}
}

func TestPostgres_AttachmentsSchemaCannotStoreSecretValues(t *testing.T) {
	db, _ := migratedAttachmentsStore(t)
	ctx := context.Background()
	rows, err := db.QueryContext(ctx, `SELECT table_name,column_name FROM information_schema.columns WHERE table_schema='attachments'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	forbidden := []string{"secret_value", "plaintext", "password", "private_key", "credential_value", "token_value"}
	for rows.Next() {
		var table, column string
		if err := rows.Scan(&table, &column); err != nil {
			t.Fatal(err)
		}
		lower := strings.ToLower(column)
		for _, word := range forbidden {
			if strings.Contains(lower, word) {
				t.Fatalf("forbidden secret-bearing column attachments.%s.%s", table, column)
			}
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}

func TestPostgres_AttachmentsProvisionBindSnapshotAcceptance(t *testing.T) {
	_, store := migratedAttachmentsStore(t)
	ctx := context.Background()
	clock := testkit.NewClock()
	envs := &testkit.Environments{Values: map[string]application.EnvironmentRef{"env-prod": {TenantID: "tenant-a", ApplicationID: "app-a", EnvironmentID: "env-prod", Name: "production", Ready: true}}}
	vault := testkit.NewVault()
	provider := &testkit.Provider{EnsureResult: application.ProviderInstanceResult{ProviderID: "provider-pg", Endpoint: "db:5432", Ready: true}, Credential: application.ProviderCredential{CredentialID: "cred-1", Values: map[string][]byte{"DATABASE_URL": []byte("postgres://private")}, Capabilities: []string{"read"}}}
	runtime := testkit.NewRuntime()
	service := &application.Service{Store: store, Environments: envs, Secrets: vault, Provider: provider, Runtime: runtime, Commerce: &testkit.Commerce{Allowed: true}, Usage: &testkit.Usage{}, Clock: clock, IDs: &application.SequentialIDs{}}
	if err := service.RegisterServicePlan(ctx, pgAttachmentPlan(t, "pg-small"), "admin"); err != nil {
		t.Fatal(err)
	}
	instance, err := service.ProvisionService(ctx, application.ProvisionServiceRequest{TenantID: "tenant-a", Name: "primary", PlanID: "pg-small", ActorID: "user-a", IdempotencyKey: "provision"})
	if err != nil {
		t.Fatal(err)
	}
	binding, snapshot, err := service.BindService(ctx, application.BindServiceRequest{TenantID: "tenant-a", ApplicationID: "app-a", EnvironmentID: "env-prod", InstanceID: instance.ID, Capabilities: []string{"read"}, ActorID: "user-a", IdempotencyKey: "bind"})
	if err != nil {
		t.Fatal(err)
	}
	if binding.State != attachmentsv1.BindingActive || snapshot.SnapshotID == "" {
		t.Fatalf("binding=%+v snapshot=%+v", binding, snapshot)
	}
	resolved, err := service.Resolve(ctx, "tenant-a", "env-prod")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(resolved)
	if strings.Contains(string(raw), "postgres://private") {
		t.Fatal("credential leaked into snapshot")
	}
	if !vault.Contains("postgres://private") {
		t.Fatal("credential was not stored in secret provider")
	}
	if runtime.Publishes != 1 {
		t.Fatalf("runtime publishes=%d", runtime.Publishes)
	}
}

func TestPostgres_AttachmentsStoreSatisfiesApplicationPort(t *testing.T) {
	_, store := migratedAttachmentsStore(t)
	var _ application.Store = store
	if store.MaxSerializableRetries < 1 {
		t.Fatal("retry budget disabled")
	}
	_ = fmt.Sprintf("%T", store)
}
