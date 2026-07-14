// Package postgres persists one-time agent enrollment and refresh credentials.
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

	"github.com/keir-research/ai-native-paas/internal/agent/enrollment"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

type Store struct{ DB *sql.DB }

func New(db *sql.DB) (*Store, error) {
	if db == nil {
		return nil, errors.New("agent enrollment postgres db is nil")
	}
	return &Store{DB: db}, nil
}

func (s *Store) Migrate(ctx context.Context) error {
	if s == nil || s.DB == nil {
		return errors.New("agent enrollment postgres db is nil")
	}
	if _, err := s.DB.ExecContext(ctx, `CREATE SCHEMA IF NOT EXISTS agent_enrollment`); err != nil {
		return err
	}
	if _, err := s.DB.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS agent_enrollment.schema_migrations(version text PRIMARY KEY, checksum text NOT NULL, applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
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
		raw, readErr := migrationFS.ReadFile("migrations/" + entry.Name())
		if readErr != nil {
			return readErr
		}
		sum := sha256.Sum256(raw)
		checksum := hex.EncodeToString(sum[:])
		var existing string
		scanErr := s.DB.QueryRowContext(ctx, `SELECT checksum FROM agent_enrollment.schema_migrations WHERE version=$1`, entry.Name()).Scan(&existing)
		if scanErr == nil {
			if existing != checksum {
				return fmt.Errorf("agent enrollment migration checksum mismatch: %s", entry.Name())
			}
			continue
		}
		if !errors.Is(scanErr, sql.ErrNoRows) {
			return scanErr
		}
		tx, beginErr := s.DB.BeginTx(ctx, nil)
		if beginErr != nil {
			return beginErr
		}
		if _, err = tx.ExecContext(ctx, string(raw)); err == nil {
			_, err = tx.ExecContext(ctx, `INSERT INTO agent_enrollment.schema_migrations(version,checksum) VALUES($1,$2)`, entry.Name(), checksum)
		}
		if err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("apply agent enrollment migration %s: %w", entry.Name(), err)
		}
		if err = tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) InsertEnrollment(ctx context.Context, record enrollment.EnrollmentRecord) error {
	if err := record.Binding.Validate(); err != nil {
		return err
	}
	scopes, err := json.Marshal(record.Binding.Scopes)
	if err != nil {
		return err
	}
	_, err = s.DB.ExecContext(ctx, `
		INSERT INTO agent_enrollment.enrollments
		(token_hash,enrollment_id,tenant_id,project_id,user_id,agent_id,scopes,expires_at,consumed_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		record.TokenHash, record.EnrollmentID, record.Binding.TenantID, record.Binding.ProjectID,
		record.Binding.UserID, record.Binding.AgentID, scopes, record.ExpiresAt.UTC(), record.ConsumedAt)
	return mapError("insert enrollment", err)
}

func (s *Store) ConsumeEnrollment(ctx context.Context, tokenHash, agentID string, now time.Time) (enrollment.EnrollmentRecord, error) {
	row := s.DB.QueryRowContext(ctx, `
		UPDATE agent_enrollment.enrollments SET consumed_at=$3
		WHERE token_hash=$1 AND agent_id=$2 AND consumed_at IS NULL AND expires_at>$3
		RETURNING enrollment_id,token_hash,tenant_id,project_id,user_id,agent_id,scopes,expires_at,consumed_at`,
		tokenHash, agentID, now.UTC())
	record, err := scanEnrollment(row)
	if errors.Is(err, sql.ErrNoRows) {
		return enrollment.EnrollmentRecord{}, errors.New("enrollment token is invalid, expired, consumed, or bound to another agent")
	}
	return record, mapError("consume enrollment", err)
}

func (s *Store) InsertRefresh(ctx context.Context, record enrollment.RefreshRecord) error {
	if err := record.Binding.Validate(); err != nil {
		return err
	}
	scopes, err := json.Marshal(record.Binding.Scopes)
	if err != nil {
		return err
	}
	_, err = s.DB.ExecContext(ctx, `
		INSERT INTO agent_enrollment.refresh_credentials
		(token_hash,credential_id,tenant_id,project_id,user_id,agent_id,scopes,public_key,expires_at,revoked_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
		record.TokenHash, record.CredentialID, record.Binding.TenantID, record.Binding.ProjectID,
		record.Binding.UserID, record.Binding.AgentID, scopes, record.PublicKey, record.ExpiresAt.UTC(), record.RevokedAt)
	return mapError("insert refresh credential", err)
}

func (s *Store) GetRefresh(ctx context.Context, tokenHash string) (enrollment.RefreshRecord, error) {
	record, err := scanRefresh(s.DB.QueryRowContext(ctx, `
		SELECT credential_id,token_hash,tenant_id,project_id,user_id,agent_id,scopes,public_key,expires_at,revoked_at
		FROM agent_enrollment.refresh_credentials WHERE token_hash=$1`, tokenHash))
	if errors.Is(err, sql.ErrNoRows) {
		return enrollment.RefreshRecord{}, errors.New("refresh credential not found")
	}
	return record, mapError("get refresh credential", err)
}

func (s *Store) RevokeRefresh(ctx context.Context, tokenHash string, now time.Time) error {
	result, err := s.DB.ExecContext(ctx, `
		UPDATE agent_enrollment.refresh_credentials SET revoked_at=COALESCE(revoked_at,$2)
		WHERE token_hash=$1`, tokenHash, now.UTC())
	if err != nil {
		return mapError("revoke refresh credential", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return errors.New("refresh credential not found")
	}
	return nil
}

type scanner interface{ Scan(...any) error }

func scanEnrollment(row scanner) (enrollment.EnrollmentRecord, error) {
	var record enrollment.EnrollmentRecord
	var binding enrollment.Binding
	var scopes []byte
	err := row.Scan(&record.EnrollmentID, &record.TokenHash, &binding.TenantID, &binding.ProjectID,
		&binding.UserID, &binding.AgentID, &scopes, &record.ExpiresAt, &record.ConsumedAt)
	if err == nil {
		err = json.Unmarshal(scopes, &binding.Scopes)
	}
	record.Binding = binding
	return record, err
}

func scanRefresh(row scanner) (enrollment.RefreshRecord, error) {
	var record enrollment.RefreshRecord
	var binding enrollment.Binding
	var scopes []byte
	err := row.Scan(&record.CredentialID, &record.TokenHash, &binding.TenantID, &binding.ProjectID,
		&binding.UserID, &binding.AgentID, &scopes, &record.PublicKey, &record.ExpiresAt, &record.RevokedAt)
	if err == nil {
		err = json.Unmarshal(scopes, &binding.Scopes)
	}
	record.Binding = binding
	return record, err
}

func mapError(operation string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", operation, err)
}
