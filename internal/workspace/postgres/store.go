// Package postgres persists disposable workspace intents and command outcomes.
package postgres

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"time"

	"github.com/keir-research/ai-native-paas/internal/workspace"
	sourcev2 "github.com/keir-research/ai-native-paas/pkg/contracts/source/v2"
	workspacev1 "github.com/keir-research/ai-native-paas/pkg/contracts/workspace/v1"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

type Store struct{ DB *sql.DB }

func (s *Store) Migrate(ctx context.Context) error {
	if s == nil || s.DB == nil {
		return errors.New("workspace postgres db is nil")
	}
	entries, err := fs.ReadDir(migrationFS, "migrations")
	if err != nil {
		return err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	if _, err := s.DB.ExecContext(ctx, `CREATE SCHEMA IF NOT EXISTS workspace; CREATE TABLE IF NOT EXISTS workspace.schema_migrations (version text PRIMARY KEY, checksum text NOT NULL, applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		raw, err := migrationFS.ReadFile("migrations/" + entry.Name())
		if err != nil {
			return err
		}
		sum := sha256.Sum256(raw)
		checksum := hex.EncodeToString(sum[:])
		var existing string
		err = s.DB.QueryRowContext(ctx, `SELECT checksum FROM workspace.schema_migrations WHERE version=$1`, entry.Name()).Scan(&existing)
		if err == nil {
			if existing != checksum {
				return fmt.Errorf("workspace migration checksum mismatch: %s", entry.Name())
			}
			continue
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		tx, err := s.DB.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('workspace-migrations', 0))`); err == nil {
			_, err = tx.ExecContext(ctx, string(raw))
		}
		if err == nil {
			_, err = tx.ExecContext(ctx, `INSERT INTO workspace.schema_migrations(version,checksum) VALUES($1,$2) ON CONFLICT(version) DO NOTHING`, entry.Name(), checksum)
		}
		if err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("apply workspace migration %s: %w", entry.Name(), err)
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

const workspaceColumns = `id,tenant_id,project_id,task_id,spec,state,idempotency_key,request_hash,correlation_id,provider_vm_id,provider_disk_ids,provider_firewall_group_ids,provider_fingerprint,last_error,expires_at,created_by,updated_by,created_at,updated_at,version,reconcile_owner,reconcile_lease_until`

func (s *Store) CreateWorkspace(ctx context.Context, candidate workspace.Workspace) (workspace.Workspace, bool, error) {
	spec, err := json.Marshal(candidate.Spec)
	if err != nil {
		return workspace.Workspace{}, false, err
	}
	disks := marshalStringArray(candidate.ProviderDiskIDs)
	firewalls := marshalStringArray(candidate.ProviderFirewallGroupIDs)
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return workspace.Workspace{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, `INSERT INTO workspace.workspaces (`+workspaceColumns+`) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22) ON CONFLICT DO NOTHING`,
		candidate.ID, candidate.TenantID, candidate.ProjectID, candidate.TaskID, spec, candidate.State, candidate.IdempotencyKey, candidate.RequestHash,
		candidate.CorrelationID, candidate.ProviderVMID, disks, firewalls, candidate.ProviderFingerprint, candidate.LastError, candidate.ExpiresAt, candidate.CreatedBy,
		candidate.UpdatedBy, candidate.CreatedAt, candidate.UpdatedAt, candidate.Version, candidate.ReconcileOwner, nullableTime(candidate.ReconcileLeaseUntil))
	if err != nil {
		return workspace.Workspace{}, false, err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return workspace.Workspace{}, false, err
	}
	created := rows == 1
	stored := candidate
	if !created {
		stored, err = scanWorkspace(tx.QueryRowContext(ctx, `SELECT `+workspaceColumns+` FROM workspace.workspaces WHERE tenant_id=$1 AND project_id=$2 AND (idempotency_key=$3 OR task_id=$4) ORDER BY (idempotency_key=$3) DESC LIMIT 1`, candidate.TenantID, candidate.ProjectID, candidate.IdempotencyKey, candidate.TaskID))
		if err != nil {
			return workspace.Workspace{}, false, mapNotFound(err)
		}
		if stored.RequestHash != candidate.RequestHash {
			return workspace.Workspace{}, false, workspace.ErrConflict
		}
	} else if err := insertOutbox(ctx, tx, "workspace", candidate.ID, candidate.Version, "workspace.reconcile.requested", candidate.ProjectID, candidate.TaskID); err != nil {
		return workspace.Workspace{}, false, err
	} else if err := insertAudit(ctx, tx, candidate.TenantID, candidate.ProjectID, candidate.CreatedBy, "workspace.create", "workspace", candidate.ID, candidate.State, candidate.CreatedAt); err != nil {
		return workspace.Workspace{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return workspace.Workspace{}, false, err
	}
	return stored, created, nil
}

func (s *Store) GetWorkspace(ctx context.Context, tenantID, projectID, workspaceID string) (workspace.Workspace, error) {
	value, err := scanWorkspace(s.DB.QueryRowContext(ctx, `SELECT `+workspaceColumns+` FROM workspace.workspaces WHERE id=$1 AND tenant_id=$2 AND project_id=$3`, workspaceID, tenantID, projectID))
	return value, mapNotFound(err)
}

func (s *Store) UpdateWorkspace(ctx context.Context, value workspace.Workspace, expected int64) error {
	disks := marshalStringArray(value.ProviderDiskIDs)
	firewalls := marshalStringArray(value.ProviderFirewallGroupIDs)
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, `UPDATE workspace.workspaces SET state=$1,provider_vm_id=$2,provider_disk_ids=$3,provider_firewall_group_ids=$4,provider_fingerprint=$5,last_error=$6,expires_at=$7,updated_by=$8,updated_at=$9,version=$10,reconcile_owner=$11,reconcile_lease_until=$12 WHERE id=$13 AND tenant_id=$14 AND project_id=$15 AND version=$16`,
		value.State, value.ProviderVMID, disks, firewalls, value.ProviderFingerprint, value.LastError, value.ExpiresAt, value.UpdatedBy, value.UpdatedAt,
		value.Version, value.ReconcileOwner, nullableTime(value.ReconcileLeaseUntil), value.ID, value.TenantID, value.ProjectID, expected)
	if err := affected(result, err); err != nil {
		return err
	}
	if value.State == workspacev1.WorkspaceDestroying {
		if err := insertOutbox(ctx, tx, "workspace", value.ID, value.Version, "workspace.reconcile.requested", value.ProjectID, value.TaskID); err != nil {
			return err
		}
	}
	if err := insertAudit(ctx, tx, value.TenantID, value.ProjectID, value.UpdatedBy, "workspace.state."+strings.ToLower(string(value.State)), "workspace", value.ID, value.State, value.UpdatedAt); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ClaimWorkspace(ctx context.Context, workspaceID, owner string, now, until time.Time) (workspace.Workspace, error) {
	value, err := scanWorkspace(s.DB.QueryRowContext(ctx, `UPDATE workspace.workspaces SET reconcile_owner=$2,reconcile_lease_until=$4,updated_by=$2,updated_at=$3,version=version+1 WHERE id=$1 AND state IN ('PROVISIONING','DESTROYING') AND (reconcile_owner='' OR reconcile_owner=$2 OR reconcile_lease_until IS NULL OR reconcile_lease_until <= $3) RETURNING `+workspaceColumns, workspaceID, owner, now, until))
	if errors.Is(err, sql.ErrNoRows) {
		existing, getErr := scanWorkspace(s.DB.QueryRowContext(ctx, `SELECT `+workspaceColumns+` FROM workspace.workspaces WHERE id=$1`, workspaceID))
		if getErr == nil && existing.State != workspacev1.WorkspaceProvisioning && existing.State != workspacev1.WorkspaceDestroying {
			return existing, nil
		}
		if getErr == nil {
			return workspace.Workspace{}, workspace.ErrReconcileClaimed
		}
		if !errors.Is(getErr, sql.ErrNoRows) {
			return workspace.Workspace{}, getErr
		}
		return workspace.Workspace{}, workspace.ErrNotFound
	}
	return value, err
}

func (s *Store) ListExpired(ctx context.Context, now time.Time, limit int) ([]workspace.Workspace, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+workspaceColumns+` FROM workspace.workspaces WHERE expires_at <= $1 AND state NOT IN ('DESTROYED','DESTROYING') ORDER BY expires_at,id LIMIT $2`, now, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []workspace.Workspace
	for rows.Next() {
		item, err := scanWorkspace(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

const commandColumns = `id,tenant_id,project_id,task_id,workspace_id,spec,kind,serialization_key,credential_leases,actor_id,idempotency_key,request_hash,state,agent_session_id,execution_vm_id,exit_code,started_at,finished_at,usage_started_at,usage_finished_at,cancel_requested_at,created_at,updated_at,version`

func (s *Store) CreateCommand(ctx context.Context, candidate workspace.Command) (workspace.Command, bool, error) {
	spec, err := json.Marshal(candidate.Spec)
	if err != nil {
		return workspace.Command{}, false, err
	}
	leases := marshalStringArray(candidate.CredentialLeases)
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return workspace.Command{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, `INSERT INTO workspace.commands (`+commandColumns+`) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24) ON CONFLICT DO NOTHING`,
		candidate.ID, candidate.TenantID, candidate.ProjectID, candidate.TaskID, candidate.WorkspaceID, spec, candidate.Kind, candidate.SerializationKey,
		leases, candidate.ActorID, candidate.IdempotencyKey, candidate.RequestHash, candidate.State, candidate.AgentSessionID, candidate.ExecutionVMID, candidate.ExitCode,
		candidate.StartedAt, candidate.FinishedAt, candidate.UsageStartedAt, candidate.UsageFinishedAt, candidate.CancelRequestedAt, candidate.CreatedAt, candidate.UpdatedAt, candidate.Version)
	if err != nil {
		return workspace.Command{}, false, err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return workspace.Command{}, false, err
	}
	created := rows == 1
	stored := candidate
	if !created {
		stored, err = scanCommand(tx.QueryRowContext(ctx, `SELECT `+commandColumns+` FROM workspace.commands WHERE tenant_id=$1 AND project_id=$2 AND idempotency_key=$3`, candidate.TenantID, candidate.ProjectID, candidate.IdempotencyKey))
		if err != nil {
			return workspace.Command{}, false, mapNotFound(err)
		}
		if stored.RequestHash != candidate.RequestHash {
			return workspace.Command{}, false, workspace.ErrConflict
		}
	} else if err := insertOutbox(ctx, tx, "command", candidate.ID, candidate.Version, "workspace.command.dispatch.requested", candidate.ProjectID, candidate.TaskID); err != nil {
		return workspace.Command{}, false, err
	} else if err := insertAudit(ctx, tx, candidate.TenantID, candidate.ProjectID, candidate.ActorID, "workspace.command.queue", "command", candidate.ID, candidate.State, candidate.CreatedAt); err != nil {
		return workspace.Command{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return workspace.Command{}, false, err
	}
	return stored, created, nil
}

func (s *Store) GetCommand(ctx context.Context, tenantID, projectID, commandID string) (workspace.Command, error) {
	value, err := scanCommand(s.DB.QueryRowContext(ctx, `SELECT `+commandColumns+` FROM workspace.commands WHERE id=$1 AND tenant_id=$2 AND project_id=$3`, commandID, tenantID, projectID))
	return value, mapNotFound(err)
}

func (s *Store) UpdateCommand(ctx context.Context, value workspace.Command, expected int64) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, `UPDATE workspace.commands SET state=$1,agent_session_id=$2,execution_vm_id=$3,exit_code=$4,started_at=$5,finished_at=$6,usage_started_at=$7,usage_finished_at=$8,cancel_requested_at=$9,updated_at=$10,version=$11 WHERE id=$12 AND tenant_id=$13 AND project_id=$14 AND version=$15`,
		value.State, value.AgentSessionID, value.ExecutionVMID, value.ExitCode, value.StartedAt, value.FinishedAt, value.UsageStartedAt,
		value.UsageFinishedAt, value.CancelRequestedAt, value.UpdatedAt, value.Version, value.ID, value.TenantID, value.ProjectID, expected)
	if err := affected(result, err); err != nil {
		return err
	}
	if err := insertAudit(ctx, tx, value.TenantID, value.ProjectID, value.ActorID, "workspace.command.state."+strings.ToLower(string(value.State)), "command", value.ID, value.State, value.UpdatedAt); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ListTimedOut(ctx context.Context, now time.Time, limit int) ([]workspace.Command, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+commandColumns+` FROM workspace.commands WHERE state='RUNNING' AND cancel_requested_at IS NULL AND started_at + ((spec->>'timeout_seconds')::bigint * interval '1 second') <= $1 ORDER BY started_at,id LIMIT $2`, now, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []workspace.Command
	for rows.Next() {
		item, err := scanCommand(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (s *Store) AcquireSerialization(ctx context.Context, projectID, key, commandID string) (bool, error) {
	result, err := s.DB.ExecContext(ctx, `INSERT INTO workspace.serialization_locks(project_id,serialization_key,command_id) VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, projectID, key, commandID)
	if err != nil {
		return false, err
	}
	rows, err := result.RowsAffected()
	if err != nil || rows == 1 {
		return rows == 1, err
	}
	var owner string
	if err := s.DB.QueryRowContext(ctx, `SELECT command_id FROM workspace.serialization_locks WHERE project_id=$1 AND serialization_key=$2`, projectID, key).Scan(&owner); err != nil {
		return false, err
	}
	return owner == commandID, nil
}

func (s *Store) ReleaseSerialization(ctx context.Context, projectID, key, commandID string) error {
	result, err := s.DB.ExecContext(ctx, `DELETE FROM workspace.serialization_locks WHERE project_id=$1 AND serialization_key=$2 AND command_id=$3`, projectID, key, commandID)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil || rows == 1 {
		return err
	}
	var exists bool
	if err := s.DB.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM workspace.serialization_locks WHERE project_id=$1 AND serialization_key=$2)`, projectID, key).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return workspace.ErrConflict
	}
	return nil
}

func (s *Store) PutCommitReceipt(ctx context.Context, scope workspace.CommitReceiptScope, receipt sourcev2.AgentCommitReceipt) error {
	if receipt.Validate() != nil {
		return workspace.ErrPolicyDenied
	}
	raw, err := json.Marshal(receipt)
	if err != nil {
		return err
	}
	result, err := s.DB.ExecContext(ctx, `
INSERT INTO workspace.commit_receipts(command_id,tenant_id,project_id,workspace_id,task_id,actor_id,receipt)
SELECT $1,$2,$3,$4,$5,$6,$7
WHERE EXISTS (
    SELECT 1 FROM workspace.commands
    WHERE id=$1 AND tenant_id=$2 AND project_id=$3 AND workspace_id=$4 AND task_id=$5 AND actor_id=$6
)
ON CONFLICT(command_id) DO NOTHING`, scope.CommandID, scope.TenantID, scope.ProjectID, scope.WorkspaceID, scope.TaskID, scope.ActorID, raw)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 1 {
		return nil
	}
	existing, err := s.GetCommitReceipt(ctx, scope.TenantID, scope.ProjectID, scope.CommandID)
	if err != nil {
		return err
	}
	existingRaw, _ := json.Marshal(existing)
	if string(existingRaw) != string(raw) {
		return workspace.ErrConflict
	}
	return nil
}

func (s *Store) GetCommitReceipt(ctx context.Context, tenantID, projectID, commandID string) (sourcev2.AgentCommitReceipt, error) {
	var raw []byte
	err := s.DB.QueryRowContext(ctx, `SELECT receipt FROM workspace.commit_receipts WHERE tenant_id=$1 AND project_id=$2 AND command_id=$3`, tenantID, projectID, commandID).Scan(&raw)
	if err != nil {
		return sourcev2.AgentCommitReceipt{}, mapNotFound(err)
	}
	var receipt sourcev2.AgentCommitReceipt
	if json.Unmarshal(raw, &receipt) != nil || receipt.Validate() != nil {
		return sourcev2.AgentCommitReceipt{}, errors.New("stored workspace commit receipt is invalid")
	}
	return receipt, nil
}

// ClaimOutbox leases unpublished intents to one worker. The row locks exist
// only for the duration of this statement; the durable lease protects the
// external effect after the transaction has committed.
func (s *Store) ClaimOutbox(ctx context.Context, owner string, now, until time.Time, limit int) ([]workspace.OutboxRecord, error) {
	if s == nil || s.DB == nil || strings.TrimSpace(owner) == "" || !until.After(now) || limit < 1 || limit > 1000 {
		return nil, errors.New("workspace outbox claim is invalid")
	}
	rows, err := s.DB.QueryContext(ctx, `
WITH candidates AS (
    SELECT event_id
    FROM workspace.outbox
    WHERE published_at IS NULL
      AND (delivery_lease_until IS NULL OR delivery_lease_until <= $2)
    ORDER BY event_id
    FOR UPDATE SKIP LOCKED
    LIMIT $4
)
UPDATE workspace.outbox AS events
SET delivery_owner=$1,
    delivery_lease_until=$3,
    delivery_attempts=events.delivery_attempts+1
FROM candidates
WHERE events.event_id=candidates.event_id
RETURNING events.event_id,events.aggregate_type,events.aggregate_id,events.aggregate_version,events.event_type,events.delivery_attempts`, owner, now, until, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	records := make([]workspace.OutboxRecord, 0)
	for rows.Next() {
		var record workspace.OutboxRecord
		if err := rows.Scan(&record.EventID, &record.AggregateType, &record.AggregateID, &record.AggregateVersion, &record.EventType, &record.DeliveryAttempts); err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, rows.Err()
}

// DeferOutbox keeps ownership while moving a readiness check to a near-future
// attempt. It never acknowledges the intent.
func (s *Store) DeferOutbox(ctx context.Context, eventID int64, owner string, until time.Time) error {
	if strings.TrimSpace(owner) == "" || eventID < 1 || until.IsZero() {
		return errors.New("workspace outbox deferral is invalid")
	}
	result, err := s.DB.ExecContext(ctx, `UPDATE workspace.outbox SET delivery_lease_until=$3 WHERE event_id=$1 AND delivery_owner=$2 AND published_at IS NULL`, eventID, owner, until)
	return affected(result, err)
}

func (s *Store) MarkOutboxPublished(ctx context.Context, eventID int64, owner string, now time.Time) error {
	if strings.TrimSpace(owner) == "" || eventID < 1 || now.IsZero() {
		return errors.New("workspace outbox acknowledgement is invalid")
	}
	result, err := s.DB.ExecContext(ctx, `UPDATE workspace.outbox SET published_at=$3,delivery_owner='',delivery_lease_until=NULL WHERE event_id=$1 AND delivery_owner=$2 AND published_at IS NULL`, eventID, owner, now)
	return affected(result, err)
}

// GetCommandForWorker is intentionally unscoped at the SQL boundary. It is
// available only to the trusted outbox worker, which derives the verified
// tenant/project/actor scope from this immutable stored command rather than
// from the event payload.
func (s *Store) GetCommandForWorker(ctx context.Context, commandID string) (workspace.Command, error) {
	if strings.TrimSpace(commandID) == "" {
		return workspace.Command{}, workspace.ErrNotFound
	}
	value, err := scanCommand(s.DB.QueryRowContext(ctx, `SELECT `+commandColumns+` FROM workspace.commands WHERE id=$1`, commandID))
	return value, mapNotFound(err)
}

type scanner interface{ Scan(...any) error }

func scanWorkspace(row scanner) (workspace.Workspace, error) {
	var value workspace.Workspace
	var spec, disks, firewalls []byte
	var lease sql.NullTime
	err := row.Scan(&value.ID, &value.TenantID, &value.ProjectID, &value.TaskID, &spec, &value.State, &value.IdempotencyKey, &value.RequestHash,
		&value.CorrelationID, &value.ProviderVMID, &disks, &firewalls, &value.ProviderFingerprint, &value.LastError, &value.ExpiresAt, &value.CreatedBy,
		&value.UpdatedBy, &value.CreatedAt, &value.UpdatedAt, &value.Version, &value.ReconcileOwner, &lease)
	if err != nil {
		return value, err
	}
	if err := json.Unmarshal(spec, &value.Spec); err != nil {
		return value, err
	}
	if err := json.Unmarshal(disks, &value.ProviderDiskIDs); err != nil {
		return value, err
	}
	if err := json.Unmarshal(firewalls, &value.ProviderFirewallGroupIDs); err != nil {
		return value, err
	}
	if lease.Valid {
		value.ReconcileLeaseUntil = lease.Time
	}
	return value, nil
}

func scanCommand(row scanner) (workspace.Command, error) {
	var value workspace.Command
	var spec, leases []byte
	var exit sql.NullInt64
	var started, finished, usageStarted, usageFinished, cancelRequested sql.NullTime
	err := row.Scan(&value.ID, &value.TenantID, &value.ProjectID, &value.TaskID, &value.WorkspaceID, &spec, &value.Kind,
		&value.SerializationKey, &leases, &value.ActorID, &value.IdempotencyKey, &value.RequestHash, &value.State, &value.AgentSessionID,
		&value.ExecutionVMID, &exit, &started, &finished, &usageStarted, &usageFinished, &cancelRequested, &value.CreatedAt, &value.UpdatedAt, &value.Version)
	if err != nil {
		return value, err
	}
	if err := json.Unmarshal(spec, &value.Spec); err != nil {
		return value, err
	}
	if err := json.Unmarshal(leases, &value.CredentialLeases); err != nil {
		return value, err
	}
	if exit.Valid {
		converted := int(exit.Int64)
		value.ExitCode = &converted
	}
	value.StartedAt, value.FinishedAt = nullTimePointer(started), nullTimePointer(finished)
	value.UsageStartedAt, value.UsageFinishedAt = nullTimePointer(usageStarted), nullTimePointer(usageFinished)
	value.CancelRequestedAt = nullTimePointer(cancelRequested)
	return value, nil
}

func insertOutbox(ctx context.Context, tx *sql.Tx, aggregateType, aggregateID string, version int64, eventType, projectID, taskID string) error {
	payload, _ := json.Marshal(map[string]string{"aggregate_id": aggregateID, "project_id": projectID, "task_id": taskID})
	_, err := tx.ExecContext(ctx, `INSERT INTO workspace.outbox(aggregate_type,aggregate_id,aggregate_version,event_type,payload) VALUES($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING`, aggregateType, aggregateID, version, eventType, payload)
	return err
}

func insertAudit(ctx context.Context, tx *sql.Tx, tenantID, projectID, actorID, action, resourceType, resourceID string, state any, occurredAt time.Time) error {
	metadata, _ := json.Marshal(map[string]any{"state": state})
	_, err := tx.ExecContext(ctx, `INSERT INTO workspace.audit(tenant_id,project_id,actor_id,action,resource_type,resource_id,metadata,occurred_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, tenantID, projectID, actorID, action, resourceType, resourceID, metadata, occurredAt)
	return err
}

func affected(result sql.Result, err error) error {
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows != 1 {
		return workspace.ErrConflict
	}
	return nil
}

func mapNotFound(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return workspace.ErrNotFound
	}
	return err
}

func nullableTime(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return value
}

func nullTimePointer(value sql.NullTime) *time.Time {
	if !value.Valid {
		return nil
	}
	return &value.Time
}

func marshalStringArray(values []string) []byte {
	if values == nil {
		values = []string{}
	}
	raw, _ := json.Marshal(values)
	return raw
}
