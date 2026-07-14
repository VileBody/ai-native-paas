package postgres_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func migration(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "migrations", "source", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
func TestMigrations_SourceSchemaOwnsItsTables(t *testing.T) {
	sql := migration(t, "001_source.sql")
	for _, table := range []string{"source.projects", "source.repositories", "source.branch_heads", "source.merge_requests", "source.workspaces", "source.idempotency", "source.webhook_receipts", "source.outbox", "source.audit"} {
		if !strings.Contains(sql, table) {
			t.Errorf("missing %s", table)
		}
	}
}
func TestMigrations_NoCrossDomainForeignKeys(t *testing.T) {
	sql := migration(t, "001_source.sql")
	for _, schema := range []string{"kernel.", "build.", "runtime.", "attachments.", "commerce.", "agent."} {
		if strings.Contains(sql, "REFERENCES "+schema) {
			t.Fatalf("cross-domain reference: %s", schema)
		}
	}
}
func TestMigrations_AuditHasMutationGuard(t *testing.T) {
	sql := migration(t, "002_audit_immutability.sql")
	if !strings.Contains(sql, "BEFORE UPDATE OR DELETE") || !strings.Contains(sql, "append-only") {
		t.Fatal("audit guard missing")
	}
}
func TestMigrations_WebhookAndIdempotencyHaveCompositePrimaryKeys(t *testing.T) {
	sql := migration(t, "001_source.sql")
	if !strings.Contains(sql, "PRIMARY KEY (provider, event_id)") || !strings.Contains(sql, "PRIMARY KEY (tenant_id, key)") {
		t.Fatal("dedupe keys missing")
	}
}

func TestMigrations_PreviewEnvironmentBindingAndDeletionAreDurable(t *testing.T) {
	sql := migration(t, "004_branch_environment_cleanup.sql")
	for _, required := range []string{"environment_id", "deleted_at", "source_branch_environment_identity_idx"} {
		if !strings.Contains(sql, required) {
			t.Fatalf("missing %s", required)
		}
	}
}
