package remotestate

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

type NamespaceOwner struct {
	Namespace string
	TenantID  string
	ProjectID string
}

type PostgresRepository struct {
	db      *sql.DB
	lockTTL time.Duration
}

func NewPostgresRepository(db *sql.DB, lockTTL time.Duration) (*PostgresRepository, error) {
	if db == nil || lockTTL < time.Minute {
		return nil, errors.New("PostgreSQL repository requires database and lock TTL of at least one minute")
	}
	return &PostgresRepository{db: db, lockTTL: lockTTL}, nil
}

func (r *PostgresRepository) Migrate(ctx context.Context) error {
	_, err := r.db.ExecContext(ctx, `
CREATE SCHEMA IF NOT EXISTS state_service;
CREATE TABLE IF NOT EXISTS state_service.namespaces (
    namespace text PRIMARY KEY,
    tenant_id text NOT NULL,
    project_id text NOT NULL,
    object_version text NOT NULL DEFAULT '',
    etag text NOT NULL DEFAULT '',
    sha256 text NOT NULL DEFAULT '',
    size_bytes bigint NOT NULL DEFAULT 0 CHECK (size_bytes >= 0),
    updated_by text NOT NULL DEFAULT '',
    updated_at timestamptz
);
CREATE TABLE IF NOT EXISTS state_service.locks (
    namespace text PRIMARY KEY REFERENCES state_service.namespaces(namespace) ON DELETE CASCADE,
    lock_id text NOT NULL,
    lock_info jsonb NOT NULL,
    actor text NOT NULL,
    acquired_at timestamptz NOT NULL,
    lease_expires_at timestamptz NOT NULL
);
CREATE INDEX IF NOT EXISTS state_locks_expiry_idx ON state_service.locks(lease_expires_at);
`)
	if err != nil {
		return fmt.Errorf("migrate state service: %w", err)
	}
	return nil
}

func (r *PostgresRepository) EnsureNamespace(ctx context.Context, owner NamespaceOwner) error {
	if err := ValidateNamespace(owner.Namespace); err != nil || owner.TenantID == "" || owner.ProjectID == "" {
		return errors.New("complete state namespace ownership is required")
	}
	result, err := r.db.ExecContext(ctx, `
INSERT INTO state_service.namespaces(namespace, tenant_id, project_id)
VALUES ($1, $2, $3)
ON CONFLICT (namespace) DO UPDATE SET
    tenant_id = EXCLUDED.tenant_id,
    project_id = EXCLUDED.project_id
WHERE state_service.namespaces.tenant_id = EXCLUDED.tenant_id
  AND state_service.namespaces.project_id = EXCLUDED.project_id`, owner.Namespace, owner.TenantID, owner.ProjectID)
	if err != nil {
		return fmt.Errorf("ensure state namespace: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("inspect state namespace ownership: %w", err)
	}
	if rows != 1 {
		return errors.New("state namespace is already owned by another project")
	}
	return nil
}

func (r *PostgresRepository) Acquire(ctx context.Context, namespace string, lock Lock, actor string) (*Lock, error) {
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, namespace); err != nil {
		return nil, err
	}
	var existingJSON []byte
	var existingID string
	err = tx.QueryRowContext(ctx, `SELECT lock_id, lock_info FROM state_service.locks WHERE namespace=$1 AND lease_expires_at > now()`, namespace).Scan(&existingID, &existingJSON)
	if err == nil && existingID != lock.ID {
		var existing Lock
		_ = json.Unmarshal(existingJSON, &existing)
		return &existing, ErrLocked
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	encoded, err := json.Marshal(lock)
	if err != nil {
		return nil, err
	}
	_, err = tx.ExecContext(ctx, `
INSERT INTO state_service.locks(namespace, lock_id, lock_info, actor, acquired_at, lease_expires_at)
VALUES ($1, $2, $3, $4, now(), now() + $5 * interval '1 second')
ON CONFLICT (namespace) DO UPDATE SET
    lock_id=EXCLUDED.lock_id, lock_info=EXCLUDED.lock_info, actor=EXCLUDED.actor,
    acquired_at=EXCLUDED.acquired_at, lease_expires_at=EXCLUDED.lease_expires_at`, namespace, lock.ID, encoded, actor, r.lockTTL.Seconds())
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return nil, nil
}

func (r *PostgresRepository) Verify(ctx context.Context, namespace, lockID string) error {
	result, err := r.db.ExecContext(ctx, `
UPDATE state_service.locks
SET lease_expires_at=now() + $3 * interval '1 second'
WHERE namespace=$1 AND lock_id=$2 AND lease_expires_at > now()`, namespace, lockID, r.lockTTL.Seconds())
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows != 1 {
		return ErrLockMismatch
	}
	return nil
}

func (r *PostgresRepository) Release(ctx context.Context, namespace, lockID string) error {
	result, err := r.db.ExecContext(ctx, `DELETE FROM state_service.locks WHERE namespace=$1 AND lock_id=$2`, namespace, lockID)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows != 1 {
		return ErrLockMismatch
	}
	return nil
}

func (r *PostgresRepository) RecordState(ctx context.Context, namespace string, blob Blob, actor string) error {
	result, err := r.db.ExecContext(ctx, `
UPDATE state_service.namespaces SET
    object_version=$2, etag=$3, sha256=$4, size_bytes=$5, updated_by=$6, updated_at=now()
WHERE namespace=$1`, namespace, blob.VersionID, blob.ETag, Digest(blob.Data), len(blob.Data), actor)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows != 1 {
		return errors.New("state namespace ownership metadata is missing")
	}
	return nil
}
