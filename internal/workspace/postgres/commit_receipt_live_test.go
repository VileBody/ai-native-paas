//go:build integration_postgres

package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/keir-research/ai-native-paas/internal/postgresbootstrap"
	"github.com/keir-research/ai-native-paas/internal/workspace"
	sourcev2 "github.com/keir-research/ai-native-paas/pkg/contracts/source/v2"
	workspacev1 "github.com/keir-research/ai-native-paas/pkg/contracts/workspace/v1"
)

func TestPostgres_WorkspaceCommitReceiptMigrationAndRoundTrip(t *testing.T) {
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db, err := postgresbootstrap.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := &Store{DB: db}
	if err := postgresbootstrap.WithMigrationLock(ctx, db, "workspace-live-test", store.Migrate); err != nil {
		t.Fatal(err)
	}
	suffix := time.Now().UTC().Format("20060102150405.000000000")
	tenantID, projectID := "live-tenant-"+suffix, "live-project-"+suffix
	workspaceID, commandID := "live-workspace-"+suffix, "live-command-"+suffix
	now := time.Now().UTC()
	workspaceValue := workspace.Workspace{
		ID: workspaceID, TenantID: tenantID, ProjectID: projectID, TaskID: "task-1",
		Spec: workspacev1.WorkspaceSpec{
			ProjectID: projectID, TaskID: "task-1", ImageDigest: "sha256:" + strings.Repeat("a", 64), CPUMillis: 1000, MemoryMiB: 1024, TTLSeconds: 600, NetworkProfile: "isolated-governed",
			SourceRevision: &sourcev2.SourceRevision{RepositoryID: "repo-1", CommitSHA: strings.Repeat("a", 40)},
		},
		State: workspacev1.WorkspaceReady, IdempotencyKey: "workspace-" + suffix, RequestHash: "hash", CorrelationID: "corr-" + suffix,
		ExpiresAt: now.Add(10 * time.Minute), CreatedBy: "agent-1", UpdatedBy: "agent-1", CreatedAt: now, UpdatedAt: now, Version: 1,
	}
	if _, _, err := store.CreateWorkspace(ctx, workspaceValue); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM workspace.commit_receipts WHERE command_id=$1`, commandID)
		_, _ = db.Exec(`DELETE FROM workspace.commands WHERE id=$1`, commandID)
		_, _ = db.Exec(`DELETE FROM workspace.workspaces WHERE id=$1`, workspaceID)
	})
	command := workspace.Command{
		ID: commandID, TenantID: tenantID, ProjectID: projectID, TaskID: "task-1", WorkspaceID: workspaceID,
		Spec: workspacev1.CommandSpec{Argv: []string{"workspace-agent", "verified-git-commit"}, TimeoutSeconds: 60, OutputLimitBytes: 4096},
		Kind: "repository_commit", SerializationKey: "repository:repo-1", ActorID: "agent-1", IdempotencyKey: "command-" + suffix,
		RequestHash: "hash", State: workspacev1.CommandRunning, AgentSessionID: "session-1", ExecutionVMID: "vm-1", CreatedAt: now, UpdatedAt: now, Version: 1,
	}
	if _, _, err := store.CreateCommand(ctx, command); err != nil {
		t.Fatal(err)
	}
	statement := sourcev2.CommitStatement{
		RepositoryID: "repo-1", BaseSHA: strings.Repeat("a", 40), CommitSHA: strings.Repeat("b", 40), Branch: "agent/task-1",
		AgentID: "agent-1", TaskID: "task-1", CorrelationID: "corr-1", IssuedAt: now,
	}
	canonical, _ := statement.Canonical()
	statementHash := sha256.Sum256(canonical)
	signature := []byte("live-signature")
	signatureHash := sha256.Sum256(signature)
	receipt := sourcev2.AgentCommitReceipt{
		SessionID: "session-1", ExecutionSessionID: "session-1", CommandID: commandID, Statement: statement,
		Attestation: sourcev2.CommitAttestation{
			RepositoryID: "repo-1", CommitSHA: statement.CommitSHA, AgentID: "agent-1", TaskID: "task-1", CorrelationID: "corr-1",
			StatementDigest: "sha256:" + hex.EncodeToString(statementHash[:]), SignatureDigest: "sha256:" + hex.EncodeToString(signatureHash[:]), IssuedAt: now,
		},
		Signature: base64.StdEncoding.EncodeToString(signature), CertificateFingerprint: "sha256:" + strings.Repeat("c", 64),
	}
	scope := workspace.CommitReceiptScope{TenantID: tenantID, ProjectID: projectID, WorkspaceID: workspaceID, TaskID: "task-1", CommandID: commandID, ActorID: "agent-1"}
	if err := store.PutCommitReceipt(ctx, scope, receipt); err != nil {
		t.Fatal(err)
	}
	stored, err := store.GetCommitReceipt(ctx, tenantID, projectID, commandID)
	if err != nil || stored.Attestation.StatementDigest != receipt.Attestation.StatementDigest {
		t.Fatalf("stored=%#v err=%v", stored, err)
	}
	if err := store.PutCommitReceipt(ctx, scope, receipt); err != nil {
		t.Fatalf("idempotent receipt replay failed: %v", err)
	}
}
