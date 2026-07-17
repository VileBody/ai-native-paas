package kernelpostgres

import (
	"context"
	"strings"
	"testing"
)

func TestPostgres_Migrations_AreOrderedAndChecksummed(t *testing.T) {
	migrations, err := Migrations()
	if err != nil {
		t.Fatal(err)
	}
	if len(migrations) != 3 {
		t.Fatalf("migration count = %d, want 3", len(migrations))
	}
	expected := []struct {
		version  int
		name     string
		checksum string
	}{
		{1, "001_initial.sql", "954611146a03378910e49c2423d83b74993519eed203ca8b7902d606359a46f9"},
		{2, "002_indexes.sql", "14c4a858c91ce3f568f0493ed849be6cde86f87154449add051ce923d292f72d"},
		{3, "003_execution_graphs.sql", "186efe0793a6d3d8459017f728b538be812d14c61f415d0df26d8caaf3ffbad9"},
	}
	for i, want := range expected {
		got := migrations[i]
		if got.Version != want.version || got.Name != want.name || got.Checksum != want.checksum {
			t.Errorf("migration[%d] = version=%d name=%s checksum=%s", i, got.Version, got.Name, got.Checksum)
		}
	}
}

func TestPostgres_ExecutionGraphMigrationIsDurableAndAppendOnly(t *testing.T) {
	migrations, err := Migrations()
	if err != nil {
		t.Fatal(err)
	}
	sql := migrations[2].SQL
	for _, fragment := range []string{
		"CREATE TABLE kernel.operation_graphs",
		"UNIQUE (tenant_id, project_id, idempotency_key)",
		"CREATE TABLE kernel.execution_audit_records",
		"BEFORE UPDATE ON kernel.execution_audit_records",
		"BEFORE DELETE ON kernel.execution_audit_records",
	} {
		if !strings.Contains(sql, fragment) {
			t.Errorf("execution migration fragment %q is missing", fragment)
		}
	}
}

func TestPostgres_MigrationDefinesAllKernelTables(t *testing.T) {
	migrations, err := Migrations()
	if err != nil {
		t.Fatal(err)
	}
	sql := migrations[0].SQL
	tables := []string{
		"kernel.organizations",
		"kernel.memberships",
		"kernel.operations",
		"kernel.idempotency_records",
		"kernel.outbox_events",
		"kernel.inbox_events",
		"kernel.audit_records",
	}
	for _, table := range tables {
		if !strings.Contains(sql, "CREATE TABLE "+table) {
			t.Errorf("initial migration does not create %s", table)
		}
	}
}

func TestPostgres_AuditMigrationRejectsUpdateAndDelete(t *testing.T) {
	migrations, err := Migrations()
	if err != nil {
		t.Fatal(err)
	}
	sql := migrations[0].SQL
	required := []string{
		"CREATE OR REPLACE FUNCTION kernel.reject_audit_mutation()",
		"BEFORE UPDATE ON kernel.audit_records",
		"BEFORE DELETE ON kernel.audit_records",
		"append-only",
	}
	for _, fragment := range required {
		if !strings.Contains(sql, fragment) {
			t.Errorf("audit immutability fragment %q is missing", fragment)
		}
	}
}

func TestPostgres_MigrationIndexesSupportDispatchAndAuditQueries(t *testing.T) {
	migrations, err := Migrations()
	if err != nil {
		t.Fatal(err)
	}
	sql := migrations[1].SQL
	for _, index := range []string{"outbox_dispatch_idx", "audit_tenant_occurred_idx", "memberships_principal_state_idx"} {
		if !strings.Contains(sql, index) {
			t.Errorf("index %s missing", index)
		}
	}
}

func TestPostgres_NewStoreAndMigrateRejectNilDatabase(t *testing.T) {
	if _, err := NewStore(nil); err == nil {
		t.Fatal("NewStore(nil) succeeded")
	}
	if err := Migrate(context.Background(), nil); err == nil {
		t.Fatal("Migrate(nil) succeeded")
	}
}
