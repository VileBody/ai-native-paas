package kernelpostgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/keir-research/ai-native-paas/internal/kernel/execution"
)

// ExecutionStore persists v2 operation graphs independently from the v1
// operation row. Checkpoints live inside the versioned graph document so the
// graph state and the exactly-once checkpoint record commit atomically.
type ExecutionStore struct {
	db *sql.DB
}

func NewExecutionStore(db *sql.DB) (*ExecutionStore, error) {
	if db == nil {
		return nil, errors.New("postgres execution store requires database")
	}
	return &ExecutionStore{db: db}, nil
}

func (s *ExecutionStore) ClaimGraph(ctx context.Context, candidate *execution.Graph) (*execution.Graph, bool, error) {
	if candidate == nil {
		return nil, false, executionError(execution.CodeInvalidArgument, "operation graph is required")
	}
	raw, err := json.Marshal(candidate)
	if err != nil {
		return nil, false, fmt.Errorf("encode operation graph: %w", err)
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return nil, false, fmt.Errorf("begin graph claim: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, `
		INSERT INTO kernel.operation_graphs(
			graph_id, tenant_id, project_id, workspace_id, idempotency_key,
			command_fingerprint, graph, version, created_at, updated_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7::jsonb,$8,$9,$10)
		ON CONFLICT (tenant_id, project_id, idempotency_key) DO NOTHING`,
		candidate.GraphID, candidate.TenantID, candidate.ProjectID, candidate.WorkspaceID,
		candidate.IdempotencyKey, candidate.CommandFingerprint, string(raw), candidate.Version,
		candidate.CreatedAt, candidate.UpdatedAt,
	)
	if err != nil {
		return nil, false, fmt.Errorf("claim operation graph: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return nil, false, fmt.Errorf("inspect graph claim: %w", err)
	}
	stored, err := getExecutionGraph(ctx, tx, `
		SELECT graph FROM kernel.operation_graphs
		WHERE tenant_id=$1 AND project_id=$2 AND idempotency_key=$3`,
		candidate.TenantID, candidate.ProjectID, candidate.IdempotencyKey)
	if err != nil {
		return nil, false, err
	}
	if err := tx.Commit(); err != nil {
		return nil, false, fmt.Errorf("commit graph claim: %w", err)
	}
	return stored, affected == 1, nil
}

func (s *ExecutionStore) GetGraph(ctx context.Context, graphID string) (*execution.Graph, error) {
	return getExecutionGraph(ctx, s.db, `SELECT graph FROM kernel.operation_graphs WHERE graph_id=$1`, graphID)
}

func (s *ExecutionStore) UpdateGraph(ctx context.Context, graphID string, expectedVersion int64, mutate func(*execution.Graph) error) (*execution.Graph, error) {
	if mutate == nil {
		return nil, executionError(execution.CodeInvalidArgument, "operation graph mutation is required")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return nil, fmt.Errorf("begin graph update: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	current, err := getExecutionGraph(ctx, tx, `SELECT graph FROM kernel.operation_graphs WHERE graph_id=$1`, graphID)
	if err != nil {
		return nil, err
	}
	if current.Version != expectedVersion {
		return nil, executionError(execution.CodeConflict, "operation graph version is stale")
	}
	candidate := current.Clone()
	if err := mutate(candidate); err != nil {
		return nil, err
	}
	if candidate.GraphID != current.GraphID || candidate.TenantID != current.TenantID ||
		candidate.ProjectID != current.ProjectID || candidate.WorkspaceID != current.WorkspaceID ||
		candidate.IdempotencyKey != current.IdempotencyKey || candidate.CommandFingerprint != current.CommandFingerprint {
		return nil, executionError(execution.CodeInvalidArgument, "operation graph identity is immutable")
	}
	if candidate.Version <= current.Version {
		return nil, executionError(execution.CodeConflict, "operation graph mutation did not advance its version")
	}
	raw, err := json.Marshal(candidate)
	if err != nil {
		return nil, fmt.Errorf("encode updated operation graph: %w", err)
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE kernel.operation_graphs
		SET graph=$2::jsonb, version=$3, updated_at=$4
		WHERE graph_id=$1 AND version=$5`,
		graphID, string(raw), candidate.Version, candidate.UpdatedAt, expectedVersion)
	if err != nil {
		return nil, fmt.Errorf("update operation graph: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return nil, fmt.Errorf("inspect graph update: %w", err)
	}
	if affected != 1 {
		return nil, executionError(execution.CodeConflict, "operation graph version is stale")
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit graph update: %w", err)
	}
	return candidate.Clone(), nil
}

func (s *ExecutionStore) AppendAudit(ctx context.Context, record execution.AuditRecord) error {
	metadata := record.Metadata
	if len(metadata) == 0 {
		metadata = json.RawMessage(`{}`)
	}
	if !json.Valid(metadata) {
		return executionError(execution.CodeInvalidArgument, "audit metadata is invalid")
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO kernel.execution_audit_records(
			audit_id, tenant_id, project_id, principal, action, outcome,
			error_code, metadata, occurred_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8::jsonb,$9)`,
		record.AuditID, record.TenantID, record.ProjectID, record.Principal, record.Action,
		record.Outcome, record.ErrorCode, string(metadata), record.OccurredAt)
	if err != nil {
		return fmt.Errorf("append execution audit: %w", err)
	}
	return nil
}

type executionGraphQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func getExecutionGraph(ctx context.Context, db executionGraphQueryer, query string, args ...any) (*execution.Graph, error) {
	var raw []byte
	if err := db.QueryRowContext(ctx, query, args...).Scan(&raw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, executionError(execution.CodeNotFound, "operation graph not found")
		}
		return nil, fmt.Errorf("read operation graph: %w", err)
	}
	var graph execution.Graph
	if err := json.Unmarshal(raw, &graph); err != nil {
		return nil, fmt.Errorf("decode operation graph: %w", err)
	}
	return graph.Clone(), nil
}

func executionError(code execution.ErrorCode, message string) error {
	return &execution.Error{Code: code, Message: message}
}
