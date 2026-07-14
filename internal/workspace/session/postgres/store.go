// Package postgres persists outbound workspace agent sessions and messages.
package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/keir-research/ai-native-paas/internal/workspace/session"
)

type Store struct{ DB *sql.DB }

const sessionColumns = `id,tenant_id,project_id,workspace_id,task_id,agent_id,vm_id,certificate_id,connected_at,last_seen_at,expires_at,closed_at,close_reason,version`
const messageColumns = `id,workspace_id,command_id,kind,payload,payload_hash,state,session_id,vm_id,delivery_attempts,delivery_lease_until,created_at,delivered_at,acknowledged_at`
const messageReturningColumns = `message.id,message.workspace_id,message.command_id,message.kind,message.payload,message.payload_hash,message.state,message.session_id,message.vm_id,message.delivery_attempts,message.delivery_lease_until,message.created_at,message.delivered_at,message.acknowledged_at`

func (s *Store) Connect(ctx context.Context, candidate session.Session) (session.Session, error) {
	if s == nil || s.DB == nil {
		return session.Session{}, errors.New("workspace session postgres db is nil")
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return session.Session{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('workspace-session:' || $1, 0))`, candidate.WorkspaceID); err != nil {
		return session.Session{}, err
	}
	existing, err := scanSession(tx.QueryRowContext(ctx, `SELECT `+sessionColumns+` FROM workspace.agent_sessions WHERE certificate_id=$1 FOR UPDATE`, candidate.CertificateID))
	if err == nil {
		if existing.TenantID != candidate.TenantID || existing.ProjectID != candidate.ProjectID || existing.WorkspaceID != candidate.WorkspaceID || existing.TaskID != candidate.TaskID || existing.AgentID != candidate.AgentID || existing.VMID != candidate.VMID || existing.ClosedAt != nil {
			return session.Session{}, session.ErrConflict
		}
		existing.LastSeenAt = candidate.LastSeenAt
		existing.ExpiresAt = candidate.ExpiresAt
		existing.Version++
		if _, err := tx.ExecContext(ctx, `UPDATE workspace.agent_sessions SET last_seen_at=$1,expires_at=$2,version=$3 WHERE id=$4`, existing.LastSeenAt, existing.ExpiresAt, existing.Version, existing.ID); err != nil {
			return session.Session{}, err
		}
		if err := tx.Commit(); err != nil {
			return session.Session{}, err
		}
		return existing, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return session.Session{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE workspace.agent_sessions SET closed_at=$1,close_reason='certificate_rotated',version=version+1 WHERE workspace_id=$2 AND closed_at IS NULL`, candidate.ConnectedAt, candidate.WorkspaceID); err != nil {
		return session.Session{}, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO workspace.agent_sessions (`+sessionColumns+`) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)`,
		candidate.ID, candidate.TenantID, candidate.ProjectID, candidate.WorkspaceID, candidate.TaskID, candidate.AgentID, candidate.VMID,
		candidate.CertificateID, candidate.ConnectedAt, candidate.LastSeenAt, candidate.ExpiresAt, candidate.ClosedAt, candidate.CloseReason, candidate.Version)
	if err != nil {
		return session.Session{}, err
	}
	if err := tx.Commit(); err != nil {
		return session.Session{}, err
	}
	return candidate, nil
}

func (s *Store) GetSession(ctx context.Context, sessionID string) (session.Session, error) {
	value, err := scanSession(s.DB.QueryRowContext(ctx, `SELECT `+sessionColumns+` FROM workspace.agent_sessions WHERE id=$1`, sessionID))
	return value, mapNotFound(err)
}

func (s *Store) ActiveForWorkspace(ctx context.Context, workspaceID, vmID string, now time.Time, idleTTL time.Duration) (session.Session, error) {
	value, err := scanSession(s.DB.QueryRowContext(ctx, `SELECT `+sessionColumns+` FROM workspace.agent_sessions WHERE workspace_id=$1 AND ($2='' OR vm_id=$2) AND closed_at IS NULL AND expires_at>$3 AND last_seen_at + ($4 * interval '1 millisecond') > $3`, workspaceID, vmID, now, idleTTL.Milliseconds()))
	return value, mapNotFound(err)
}

func (s *Store) Touch(ctx context.Context, sessionID, certificateID string, now time.Time) (session.Session, error) {
	value, err := scanSession(s.DB.QueryRowContext(ctx, `UPDATE workspace.agent_sessions SET last_seen_at=$1,version=version+1 WHERE id=$2 AND certificate_id=$3 AND closed_at IS NULL AND expires_at>$1 RETURNING `+sessionColumns, now, sessionID, certificateID))
	return value, mapNotFound(err)
}

func (s *Store) CloseWorkspace(ctx context.Context, workspaceID string, now time.Time, reason string) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE workspace.agent_sessions SET closed_at=$1,close_reason=$2,version=version+1 WHERE workspace_id=$3 AND closed_at IS NULL`, now, reason, workspaceID)
	return err
}

func (s *Store) Queue(ctx context.Context, candidate session.Message) (session.Message, bool, error) {
	payload, err := json.Marshal(candidate.Payload)
	if err != nil {
		return session.Message{}, false, err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return session.Message{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, `INSERT INTO workspace.agent_messages (`+messageColumns+`) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14) ON CONFLICT(workspace_id,command_id,kind) DO NOTHING`,
		candidate.ID, candidate.WorkspaceID, candidate.CommandID, candidate.Kind, payload, candidate.PayloadHash, candidate.State,
		candidate.SessionID, candidate.VMID, candidate.DeliveryAttempts, candidate.DeliveryLeaseUntil, candidate.CreatedAt, candidate.DeliveredAt, candidate.AcknowledgedAt)
	if err != nil {
		return session.Message{}, false, err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return session.Message{}, false, err
	}
	created := rows == 1
	stored := candidate
	if !created {
		stored, err = scanMessage(tx.QueryRowContext(ctx, `SELECT `+messageColumns+` FROM workspace.agent_messages WHERE workspace_id=$1 AND command_id=$2 AND kind=$3`, candidate.WorkspaceID, candidate.CommandID, candidate.Kind))
		if err != nil {
			return session.Message{}, false, err
		}
		if stored.PayloadHash != candidate.PayloadHash {
			return session.Message{}, false, session.ErrConflict
		}
	}
	if err := tx.Commit(); err != nil {
		return session.Message{}, false, err
	}
	return stored, created, nil
}

func (s *Store) GetMessage(ctx context.Context, messageID string) (session.Message, error) {
	value, err := scanMessage(s.DB.QueryRowContext(ctx, `SELECT `+messageColumns+` FROM workspace.agent_messages WHERE id=$1`, messageID))
	return value, mapNotFound(err)
}

func (s *Store) ClaimNext(ctx context.Context, sessionID, workspaceID string, now, leaseUntil time.Time) (session.Message, error) {
	value, err := scanMessage(s.DB.QueryRowContext(ctx, `
WITH active_session AS (
    SELECT id,vm_id FROM workspace.agent_sessions
    WHERE id=$1 AND workspace_id=$2 AND closed_at IS NULL AND expires_at>$3
), candidate AS (
    SELECT id FROM workspace.agent_messages
    WHERE workspace_id=$2 AND (state='QUEUED' OR (state='DELIVERED' AND delivery_lease_until <= $3))
    ORDER BY created_at,id FOR UPDATE SKIP LOCKED LIMIT 1
)
UPDATE workspace.agent_messages AS message
SET state='DELIVERED',session_id=active_session.id,vm_id=active_session.vm_id,
    delivery_attempts=message.delivery_attempts+1,delivery_lease_until=$4,delivered_at=$3,acknowledged_at=NULL
FROM active_session,candidate WHERE message.id=candidate.id
RETURNING `+messageReturningColumns, sessionID, workspaceID, now, leaseUntil))
	return value, mapNotFound(err)
}

func (s *Store) Acknowledge(ctx context.Context, sessionID, workspaceID, messageID string, accepted bool, now time.Time) (session.Message, error) {
	wanted := session.MessageRejected
	if accepted {
		wanted = session.MessageAcked
	}
	value, err := scanMessage(s.DB.QueryRowContext(ctx, `UPDATE workspace.agent_messages SET state=$1,delivery_lease_until=NULL,acknowledged_at=$2 WHERE id=$3 AND workspace_id=$4 AND session_id=$5 AND state='DELIVERED' RETURNING `+messageColumns, wanted, now, messageID, workspaceID, sessionID))
	if err == nil {
		return value, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return session.Message{}, err
	}
	existing, getErr := s.GetMessage(ctx, messageID)
	if getErr != nil {
		return session.Message{}, getErr
	}
	if existing.WorkspaceID == workspaceID && existing.SessionID == sessionID && existing.State == wanted {
		return existing, nil
	}
	return session.Message{}, session.ErrConflict
}

type scanner interface{ Scan(...any) error }

func scanSession(row scanner) (session.Session, error) {
	var value session.Session
	var closed sql.NullTime
	err := row.Scan(&value.ID, &value.TenantID, &value.ProjectID, &value.WorkspaceID, &value.TaskID, &value.AgentID, &value.VMID,
		&value.CertificateID, &value.ConnectedAt, &value.LastSeenAt, &value.ExpiresAt, &closed, &value.CloseReason, &value.Version)
	if closed.Valid {
		value.ClosedAt = &closed.Time
	}
	return value, err
}

func scanMessage(row scanner) (session.Message, error) {
	var value session.Message
	var payload []byte
	var lease, delivered, acknowledged sql.NullTime
	err := row.Scan(&value.ID, &value.WorkspaceID, &value.CommandID, &value.Kind, &payload, &value.PayloadHash, &value.State,
		&value.SessionID, &value.VMID, &value.DeliveryAttempts, &lease, &value.CreatedAt, &delivered, &acknowledged)
	if err != nil {
		return value, err
	}
	if err := json.Unmarshal(payload, &value.Payload); err != nil {
		return value, err
	}
	value.DeliveryLeaseUntil = nullTime(lease)
	value.DeliveredAt = nullTime(delivered)
	value.AcknowledgedAt = nullTime(acknowledged)
	return value, nil
}

func nullTime(value sql.NullTime) *time.Time {
	if !value.Valid {
		return nil
	}
	return &value.Time
}

func mapNotFound(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return session.ErrNotFound
	}
	return err
}
