package postgres_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func buildMigration(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "migrations", "build", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestMigrations_BuildSchemaOwnsSupplyChainTables(t *testing.T) {
	sql := buildMigration(t, "001_build.sql")
	for _, table := range []string{"build.builds", "build.artifacts", "build.scan_results", "build.signature_records", "build.log_refs", "build.idempotency", "build.outbox", "build.audit"} {
		if !strings.Contains(sql, table) {
			t.Errorf("missing %s", table)
		}
	}
}

func TestMigrations_BuildHasNoCrossDomainForeignKeys(t *testing.T) {
	upper := strings.ToUpper(buildMigration(t, "001_build.sql"))
	for _, schema := range []string{"KERNEL.", "SOURCE.", "RUNTIME.", "ATTACHMENTS.", "COMMERCE.", "AGENT."} {
		if strings.Contains(upper, "REFERENCES "+schema) {
			t.Fatalf("cross-domain reference: %s", schema)
		}
	}
}

func TestMigrations_BuildIdentityAllowsFailedRetryButOnlyOneLiveAttempt(t *testing.T) {
	sql := buildMigration(t, "001_build.sql")
	for _, fragment := range []string{"UNIQUE (tenant_id, identity, attempt)", "build_one_active_or_successful_identity", "FAILED_USER_CODE", "FAILED_PLATFORM", "TIMED_OUT"} {
		if !strings.Contains(sql, fragment) {
			t.Fatalf("missing retry/identity guard %q", fragment)
		}
	}
}

func TestMigrations_BuildAuditAndArtifactIdentityAreGuarded(t *testing.T) {
	audit := buildMigration(t, "002_audit_immutability.sql")
	identity := buildMigration(t, "003_artifact_identity.sql")
	if !strings.Contains(audit, "BEFORE UPDATE OR DELETE") || !strings.Contains(audit, "append-only") {
		t.Fatal("audit mutation guard missing")
	}
	if !strings.Contains(identity, "artifact identity is immutable") || !strings.Contains(identity, "signature digest must equal artifact digest") {
		t.Fatal("artifact/signature guard missing")
	}
}

func TestMigrations_BuildEmbeddedCopiesMatchRepositoryMigrations(t *testing.T) {
	entries, err := os.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		embedded, err := os.ReadFile(filepath.Join("migrations", entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		repository, err := os.ReadFile(filepath.Join("..", "..", "..", "migrations", "build", entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if string(embedded) != string(repository) {
			t.Errorf("embedded migration %s differs from repository copy", entry.Name())
		}
		seen++
	}
	if seen == 0 {
		t.Fatal("no build migrations compared")
	}
}

func TestMigrations_BuildExecutionAndTrustGuardsArePresent(t *testing.T) {
	sql := buildMigration(t, "004_execution_and_trust_guards.sql")
	for _, fragment := range []string{
		"build identity is immutable",
		"build execution selection is immutable",
		"artifact trust records are immutable",
		"releasable artifact trust chain is incomplete",
		"artifact version must advance",
	} {
		if !strings.Contains(sql, fragment) {
			t.Errorf("missing execution/trust guard %q", fragment)
		}
	}
}

func TestMigrations_BuildArtifactsRequireProvenanceForRelease(t *testing.T) {
	create := buildMigration(t, "001_build.sql")
	provenance := buildMigration(t, "006_artifact_provenance.sql")
	for _, fragment := range []string{
		"provenance_digest text NOT NULL DEFAULT ''",
		"provenance_media_type text NOT NULL DEFAULT ''",
	} {
		if !strings.Contains(create, fragment) || !strings.Contains(provenance, fragment) {
			t.Errorf("missing provenance artifact column %q", fragment)
		}
	}
	for _, fragment := range []string{
		"artifact provenance reference is immutable",
		"provenance can only attach after signature",
		"NEW.provenance_digest = ''",
		"NEW.provenance_media_type = ''",
		"releasable artifact trust chain is incomplete",
	} {
		if !strings.Contains(provenance, fragment) {
			t.Errorf("missing provenance trust guard %q", fragment)
		}
	}
}
