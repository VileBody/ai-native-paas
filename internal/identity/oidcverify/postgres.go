package oidcverify

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strings"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

type PostgresResolver struct{ DB *sql.DB }

func NewPostgresResolver(db *sql.DB) (*PostgresResolver, error) {
	if db == nil {
		return nil, errors.New("identity postgres db is nil")
	}
	return &PostgresResolver{DB: db}, nil
}

func (r *PostgresResolver) Migrate(ctx context.Context) error {
	if r == nil || r.DB == nil {
		return errors.New("identity postgres db is nil")
	}
	if _, err := r.DB.ExecContext(ctx, `CREATE SCHEMA IF NOT EXISTS platform_identity`); err != nil {
		return err
	}
	if _, err := r.DB.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS platform_identity.schema_migrations(version text PRIMARY KEY, checksum text NOT NULL, applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
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
		scanErr := r.DB.QueryRowContext(ctx, `SELECT checksum FROM platform_identity.schema_migrations WHERE version=$1`, entry.Name()).Scan(&existing)
		if scanErr == nil {
			if existing != checksum {
				return fmt.Errorf("identity migration checksum mismatch: %s", entry.Name())
			}
			continue
		}
		if !errors.Is(scanErr, sql.ErrNoRows) {
			return scanErr
		}
		tx, beginErr := r.DB.BeginTx(ctx, nil)
		if beginErr != nil {
			return beginErr
		}
		if _, err = tx.ExecContext(ctx, string(raw)); err == nil {
			_, err = tx.ExecContext(ctx, `INSERT INTO platform_identity.schema_migrations(version,checksum) VALUES($1,$2)`, entry.Name(), checksum)
		}
		if err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("apply identity migration %s: %w", entry.Name(), err)
		}
		if err = tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

func (r *PostgresResolver) ResolveMembership(ctx context.Context, issuer, subject string) (Membership, error) {
	rows, err := r.DB.QueryContext(ctx, `
		SELECT m.tenant_id,s.user_id,m.role
		FROM platform_identity.oidc_subjects s
		JOIN platform_identity.tenant_memberships m ON m.user_id=s.user_id AND m.state='ACTIVE'
		WHERE s.issuer=$1 AND s.subject=$2
		ORDER BY m.tenant_id LIMIT 2`, issuer, subject)
	if err != nil {
		return Membership{}, err
	}
	defer rows.Close()
	var memberships []Membership
	for rows.Next() {
		var membership Membership
		if err := rows.Scan(&membership.TenantID, &membership.UserID, &membership.Role); err != nil {
			return Membership{}, err
		}
		memberships = append(memberships, membership)
	}
	if err := rows.Err(); err != nil {
		return Membership{}, err
	}
	if len(memberships) != 1 {
		return Membership{}, errors.New("OIDC subject must have exactly one active beta membership")
	}
	return memberships[0], nil
}
