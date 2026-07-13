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

	"github.com/keir-research/ai-native-paas/internal/agent/application"
	"github.com/keir-research/ai-native-paas/internal/agent/domain"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

type Store struct {
	DB                     *sql.DB
	MaxSerializableRetries int
}

func NewStore(db *sql.DB) (*Store, error) {
	if db == nil {
		return nil, errors.New("postgres db is nil")
	}
	return &Store{DB: db, MaxSerializableRetries: 8}, nil
}

func (s *Store) Migrate(ctx context.Context) error {
	if s == nil || s.DB == nil {
		return errors.New("postgres db is nil")
	}
	if _, err := s.DB.ExecContext(ctx, `CREATE SCHEMA IF NOT EXISTS agent`); err != nil {
		return err
	}
	if _, err := s.DB.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS agent.schema_migrations(version text PRIMARY KEY,checksum text NOT NULL,applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		return err
	}
	entries, err := fs.ReadDir(migrationFS, "migrations")
	if err != nil {
		return err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
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
		err = s.DB.QueryRowContext(ctx, `SELECT checksum FROM agent.schema_migrations WHERE version=$1`, entry.Name()).Scan(&existing)
		if err == nil {
			if existing != checksum {
				return fmt.Errorf("agent migration checksum mismatch: %s", entry.Name())
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
		if _, err = tx.ExecContext(ctx, string(raw)); err == nil {
			_, err = tx.ExecContext(ctx, `INSERT INTO agent.schema_migrations(version,checksum) VALUES($1,$2)`, entry.Name(), checksum)
		}
		if err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("apply agent migration %s: %w", entry.Name(), err)
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) Transact(ctx context.Context, fn func(application.Tx) error) error {
	if s == nil || s.DB == nil {
		return domain.NewError(domain.CodeUnavailable, "postgres db is nil")
	}
	attempts := s.MaxSerializableRetries
	if attempts <= 0 {
		attempts = 8
	}
	var last error
	for attempt := 0; attempt < attempts; attempt++ {
		tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
		if err != nil {
			return domain.Wrap(domain.CodeUnavailable, "begin agent transaction", err)
		}
		adapter := &txAdapter{tx: tx}
		err = fn(adapter)
		if err == nil && adapter.err != nil {
			err = adapter.err
		}
		if err != nil {
			_ = tx.Rollback()
			if !retryableDB(err) {
				return err
			}
			last = err
		} else if err = tx.Commit(); err == nil {
			return nil
		} else if !retryableDB(err) {
			return mapDB(err)
		} else {
			last = err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(attempt+1) * 5 * time.Millisecond):
		}
	}
	return domain.Wrap(domain.CodeUnavailable, "serializable transaction retry budget exhausted", last)
}

func retryableDB(err error) bool {
	// Domain errors intentionally expose a stable public message, while the SQLSTATE
	// remains in the wrapped cause. Walk the complete error chain so a mapped
	// unique/serialization failure can still trigger the bounded transaction retry.
	var parts []string
	for current := err; current != nil; current = errors.Unwrap(current) {
		parts = append(parts, current.Error())
	}
	value := strings.ToLower(strings.Join(parts, " "))
	return strings.Contains(value, "40001") || strings.Contains(value, "40p01") || strings.Contains(value, "serialization") || strings.Contains(value, "deadlock") || strings.Contains(value, "23505") || strings.Contains(value, "duplicate key")
}
func mapDB(err error) error {
	if err == nil {
		return nil
	}
	value := strings.ToLower(fmt.Sprint(err))
	switch {
	case strings.Contains(value, "23505") || strings.Contains(value, "duplicate key"):
		return domain.Wrap(domain.CodeConflict, "agent unique constraint violated", err)
	case strings.Contains(value, "55000") || strings.Contains(value, "append-only") || strings.Contains(value, "immutable"):
		return domain.Wrap(domain.CodeConflict, "immutable agent record", err)
	case strings.Contains(value, "23514") || strings.Contains(value, "payload drift") || strings.Contains(value, "check constraint"):
		return domain.Wrap(domain.CodeInvalidArgument, "agent database constraint violated", err)
	default:
		return domain.Wrap(domain.CodeUnavailable, "agent database operation failed", err)
	}
}
func affected(result sql.Result, err error, name string) error {
	if err != nil {
		return mapDB(err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return domain.NewError(domain.CodeConflict, name+" version is stale")
	}
	return nil
}

type txAdapter struct {
	tx  *sql.Tx
	err error
}

func (a *txAdapter) capture(err error) {
	if err != nil && a.err == nil {
		a.err = mapDB(err)
	}
}
func marshal(v any) ([]byte, error) { return json.Marshal(v) }
func scanPayload[T any](row interface{ Scan(...any) error }) (T, error) {
	var raw []byte
	if err := row.Scan(&raw); err != nil {
		var zero T
		return zero, err
	}
	var value T
	return value, json.Unmarshal(raw, &value)
}

func (a *txAdapter) GetPrincipal(id string) (domain.AgentPrincipal, bool) {
	v, err := scanPayload[domain.AgentPrincipal](a.tx.QueryRow(`SELECT payload FROM agent.principals WHERE id=$1`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return v, false
	}
	a.capture(err)
	return v, err == nil
}
func (a *txAdapter) InsertPrincipal(v domain.AgentPrincipal) error {
	raw, err := marshal(v)
	if err != nil {
		return err
	}
	_, err = a.tx.Exec(`INSERT INTO agent.principals(id,tenant_id,on_behalf_of_user_id,state,version,payload,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, v.ID, v.TenantID, v.OnBehalfOfUserID, v.State, v.Version, raw, v.CreatedAt, v.UpdatedAt)
	return mapDB(err)
}
func (a *txAdapter) UpdatePrincipal(v domain.AgentPrincipal, expected int64) error {
	raw, err := marshal(v)
	if err != nil {
		return err
	}
	res, err := a.tx.Exec(`UPDATE agent.principals SET state=$1,version=$2,payload=$3,updated_at=$4 WHERE id=$5 AND version=$6`, v.State, v.Version, raw, v.UpdatedAt, v.ID, expected)
	return affected(res, err, "principal")
}
func (a *txAdapter) GetTask(id string) (domain.AgentTask, bool) {
	v, err := scanPayload[domain.AgentTask](a.tx.QueryRow(`SELECT payload FROM agent.tasks WHERE id=$1`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return v, false
	}
	a.capture(err)
	return v, err == nil
}
func (a *txAdapter) InsertTask(v domain.AgentTask) error {
	raw, err := marshal(v)
	if err != nil {
		return err
	}
	_, err = a.tx.Exec(`INSERT INTO agent.tasks(id,tenant_id,agent_id,state,version,payload,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, v.ID, v.TenantID, v.AgentID, v.State, v.Version, raw, v.CreatedAt, v.UpdatedAt)
	return mapDB(err)
}
func (a *txAdapter) UpdateTask(v domain.AgentTask, e int64) error {
	raw, err := marshal(v)
	if err != nil {
		return err
	}
	res, err := a.tx.Exec(`UPDATE agent.tasks SET state=$1,version=$2,payload=$3,updated_at=$4 WHERE id=$5 AND version=$6`, v.State, v.Version, raw, v.UpdatedAt, v.ID, e)
	return affected(res, err, "task")
}
func (a *txAdapter) FindInvocation(tenant, task, key string) (domain.Invocation, bool) {
	v, err := scanPayload[domain.Invocation](a.tx.QueryRow(`SELECT payload FROM agent.invocations WHERE tenant_id=$1 AND task_id=$2 AND idempotency_key=$3`, tenant, task, key))
	if errors.Is(err, sql.ErrNoRows) {
		return v, false
	}
	a.capture(err)
	return v, err == nil
}
func (a *txAdapter) GetInvocation(id string) (domain.Invocation, bool) {
	v, err := scanPayload[domain.Invocation](a.tx.QueryRow(`SELECT payload FROM agent.invocations WHERE id=$1`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return v, false
	}
	a.capture(err)
	return v, err == nil
}
func (a *txAdapter) InsertInvocation(v domain.Invocation) error {
	raw, err := marshal(v)
	if err != nil {
		return err
	}
	_, err = a.tx.Exec(`INSERT INTO agent.invocations(id,tenant_id,agent_id,task_id,tool,idempotency_key,fingerprint,state,external_operation_id,version,payload,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`, v.ID, v.TenantID, v.AgentID, v.TaskID, v.Tool, v.IdempotencyKey, v.Fingerprint, v.State, v.ExternalOperationID, v.Version, raw, v.CreatedAt, v.UpdatedAt)
	return mapDB(err)
}
func (a *txAdapter) UpdateInvocation(v domain.Invocation, e int64) error {
	raw, err := marshal(v)
	if err != nil {
		return err
	}
	res, err := a.tx.Exec(`UPDATE agent.invocations SET state=$1,external_operation_id=$2,version=$3,payload=$4,updated_at=$5 WHERE id=$6 AND version=$7`, v.State, v.ExternalOperationID, v.Version, raw, v.UpdatedAt, v.ID, e)
	return affected(res, err, "invocation")
}
func (a *txAdapter) GetApprovalRequest(id string) (domain.ApprovalRequest, bool) {
	v, err := scanPayload[domain.ApprovalRequest](a.tx.QueryRow(`SELECT payload FROM agent.approval_requests WHERE id=$1`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return v, false
	}
	a.capture(err)
	return v, err == nil
}
func (a *txAdapter) InsertApprovalRequest(v domain.ApprovalRequest) error {
	raw, err := marshal(v)
	if err != nil {
		return err
	}
	_, err = a.tx.Exec(`INSERT INTO agent.approval_requests(id,tenant_id,agent_id,task_id,action,resource_type,resource_id,payload_hash,state,expires_at,version,payload,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)`, v.ID, v.TenantID, v.AgentID, v.TaskID, v.Action, v.Resource.Type, v.Resource.ID, v.PayloadHash, v.State, v.ExpiresAt, v.Version, raw, v.CreatedAt, v.UpdatedAt)
	return mapDB(err)
}
func (a *txAdapter) UpdateApprovalRequest(v domain.ApprovalRequest, e int64) error {
	raw, err := marshal(v)
	if err != nil {
		return err
	}
	res, err := a.tx.Exec(`UPDATE agent.approval_requests SET state=$1,version=$2,payload=$3,updated_at=$4 WHERE id=$5 AND version=$6`, v.State, v.Version, raw, v.UpdatedAt, v.ID, e)
	return affected(res, err, "approval request")
}
func (a *txAdapter) GetApprovalGrant(id string) (domain.ApprovalGrant, bool) {
	v, err := scanPayload[domain.ApprovalGrant](a.tx.QueryRow(`SELECT payload FROM agent.approval_grants WHERE id=$1`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return v, false
	}
	a.capture(err)
	return v, err == nil
}
func (a *txAdapter) InsertApprovalGrant(v domain.ApprovalGrant) error {
	raw, err := marshal(v)
	if err != nil {
		return err
	}
	_, err = a.tx.Exec(`INSERT INTO agent.approval_grants(id,request_id,tenant_id,agent_id,task_id,approver_user_id,action,resource_type,resource_id,payload_hash,expires_at,consumed_at,version,payload,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)`, v.ID, v.RequestID, v.TenantID, v.AgentID, v.TaskID, v.ApproverUserID, v.Action, v.Resource.Type, v.Resource.ID, v.PayloadHash, v.ExpiresAt, v.ConsumedAt, v.Version, raw, v.CreatedAt)
	return mapDB(err)
}
func (a *txAdapter) UpdateApprovalGrant(v domain.ApprovalGrant, e int64) error {
	raw, err := marshal(v)
	if err != nil {
		return err
	}
	res, err := a.tx.Exec(`UPDATE agent.approval_grants SET consumed_at=$1,version=$2,payload=$3 WHERE id=$4 AND version=$5`, v.ConsumedAt, v.Version, raw, v.ID, e)
	return affected(res, err, "approval grant")
}
func (a *txAdapter) AppendAudit(v domain.AuditRecord) error {
	raw, err := marshal(v)
	if err != nil {
		return err
	}
	_, err = a.tx.Exec(`INSERT INTO agent.audit(id,tenant_id,task_id,agent_id,tool,outcome,payload,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, v.ID, v.TenantID, v.TaskID, v.AgentID, v.Tool, v.Outcome, raw, v.CreatedAt)
	return mapDB(err)
}
func (a *txAdapter) ListAudit(tenant, task string) []domain.AuditRecord {
	rows, err := a.tx.Query(`SELECT payload FROM agent.audit WHERE tenant_id=$1 AND task_id=$2 ORDER BY created_at,id`, tenant, task)
	if err != nil {
		a.capture(err)
		return nil
	}
	defer rows.Close()
	out := []domain.AuditRecord{}
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			a.capture(err)
			return nil
		}
		var v domain.AuditRecord
		if err := json.Unmarshal(raw, &v); err != nil {
			a.capture(err)
			return nil
		}
		out = append(out, v)
	}
	a.capture(rows.Err())
	return out
}
func (a *txAdapter) AppendOutbox(v domain.OutboxRecord) error {
	raw, err := json.Marshal(v.Payload)
	if err != nil {
		return err
	}
	_, err = a.tx.Exec(`INSERT INTO agent.outbox(id,tenant_id,topic,aggregate_id,payload,created_at) VALUES($1,$2,$3,$4,$5,$6)`, v.ID, v.TenantID, v.Topic, v.AggregateID, raw, v.CreatedAt)
	return mapDB(err)
}

var _ application.Store = (*Store)(nil)
