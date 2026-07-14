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

	infraapp "github.com/keir-research/ai-native-paas/internal/infrastructure/application"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

type Store struct{ DB *sql.DB }

func (s *Store) Migrate(ctx context.Context) error {
	if s == nil || s.DB == nil {
		return errors.New("infrastructure postgres db is nil")
	}
	entries, err := fs.ReadDir(migrationFS, "migrations")
	if err != nil {
		return err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	if _, err = s.DB.ExecContext(ctx, `CREATE SCHEMA IF NOT EXISTS infrastructure; CREATE TABLE IF NOT EXISTS infrastructure.schema_migrations (version text PRIMARY KEY, checksum text NOT NULL, applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		return err
	}
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
		scanErr := s.DB.QueryRowContext(ctx, `SELECT checksum FROM infrastructure.schema_migrations WHERE version=$1`, entry.Name()).Scan(&existing)
		if scanErr == nil {
			if existing != checksum {
				return fmt.Errorf("infrastructure migration checksum mismatch: %s", entry.Name())
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
		if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('infrastructure-migrations', 0))`); err == nil {
			_, err = tx.ExecContext(ctx, string(raw))
		}
		if err == nil {
			_, err = tx.ExecContext(ctx, `INSERT INTO infrastructure.schema_migrations(version,checksum) VALUES($1,$2) ON CONFLICT(version) DO NOTHING`, entry.Name(), checksum)
		}
		if err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("apply infrastructure migration %s: %w", entry.Name(), err)
		}
		if err = tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

const planColumns = `id,tenant_id,project_id,workspace_id,source_sha,plan_hash,state_generation,changes,destructive,requires_approval,estimate_version,estimate_fingerprint,artifact_digest,target,idempotency_key,idempotency_fingerprint,estimate,reservation,apply_started_at,created_at,version`

func (s *Store) CreatePlan(ctx context.Context, record infraapp.PlanRecord) (infraapp.PlanRecord, error) {
	changes, estimate, reservation, err := marshalPlan(record)
	if err != nil {
		return infraapp.PlanRecord{}, err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return infraapp.PlanRecord{}, err
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, `INSERT INTO infrastructure.plans (`+planColumns+`) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21) ON CONFLICT DO NOTHING`,
		record.Summary.PlanID, record.TenantID, record.Summary.ProjectID, record.Summary.WorkspaceID,
		record.Summary.SourceSHA, record.Summary.PlanHash, record.Summary.StateGeneration, changes,
		record.Summary.Destructive, record.Summary.RequiresApproval, record.Summary.EstimateVersion,
		record.Summary.EstimateFingerprint, record.ArtifactDigest, record.Target, record.IdempotencyKey,
		record.IdempotencyFingerprint, estimate, reservation, nullableTime(record.ApplyStartedAt),
		record.Summary.CreatedAt, record.Version)
	if err != nil {
		return infraapp.PlanRecord{}, err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return infraapp.PlanRecord{}, err
	}
	stored := record
	if rows == 0 {
		stored, err = scanPlan(tx.QueryRowContext(ctx, `SELECT `+planColumns+` FROM infrastructure.plans WHERE tenant_id=$1 AND project_id=$2 AND idempotency_key=$3`, record.TenantID, record.Summary.ProjectID, record.IdempotencyKey))
		if err != nil {
			return infraapp.PlanRecord{}, mapNotFound(err)
		}
		if stored.IdempotencyFingerprint != record.IdempotencyFingerprint {
			return infraapp.PlanRecord{}, infraapp.ErrConflict
		}
	} else if err = audit(ctx, tx, record.TenantID, record.Summary.ProjectID, "system", "infrastructure.plan.created", record.Summary.PlanID, map[string]any{"plan_hash": record.Summary.PlanHash}, record.Summary.CreatedAt); err != nil {
		return infraapp.PlanRecord{}, err
	}
	if err = tx.Commit(); err != nil {
		return infraapp.PlanRecord{}, err
	}
	return stored, nil
}

func (s *Store) GetPlan(ctx context.Context, tenantID, projectID, planID string) (infraapp.PlanRecord, error) {
	value, err := scanPlan(s.DB.QueryRowContext(ctx, `SELECT `+planColumns+` FROM infrastructure.plans WHERE id=$1 AND tenant_id=$2 AND project_id=$3`, planID, tenantID, projectID))
	return value, mapNotFound(err)
}

func (s *Store) CreateApproval(ctx context.Context, grant infraapp.ApprovalGrant) (infraapp.ApprovalGrant, error) {
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return infraapp.ApprovalGrant{}, err
	}
	defer func() { _ = tx.Rollback() }()
	plan, err := scanPlan(tx.QueryRowContext(ctx, `SELECT `+planColumns+` FROM infrastructure.plans WHERE id=$1 AND tenant_id=$2 AND project_id=$3 FOR SHARE`, grant.PlanID, grant.TenantID, grant.ProjectID))
	if err != nil {
		return infraapp.ApprovalGrant{}, mapNotFound(err)
	}
	if !plan.Summary.RequiresApproval || plan.Summary.PlanHash != grant.PlanHash || plan.Estimate.Version != grant.EstimateVersion || plan.Reservation.ReservationID != grant.ReservationID || plan.Target != grant.Target || grant.CreatedAt.IsZero() || !grant.ExpiresAt.After(grant.CreatedAt) || grant.ExpiresAt.After(plan.Reservation.ExpiresAt) {
		return infraapp.ApprovalGrant{}, infraapp.ErrPermissionDenied
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO infrastructure.approval_grants(id,tenant_id,project_id,plan_id,plan_hash,estimate_version,reservation_id,target,actor_id,approver_user_id,created_at,expires_at,consumed_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,NULL)`,
		grant.GrantID, grant.TenantID, grant.ProjectID, grant.PlanID, grant.PlanHash, grant.EstimateVersion,
		grant.ReservationID, grant.Target, grant.ActorID, grant.ApproverUserID, grant.CreatedAt, grant.ExpiresAt)
	if err != nil {
		return infraapp.ApprovalGrant{}, mapConflict(err)
	}
	if err = audit(ctx, tx, grant.TenantID, grant.ProjectID, grant.ApproverUserID, "infrastructure.approval.granted", grant.PlanID, map[string]any{"grant_id": grant.GrantID}, grant.CreatedAt); err != nil {
		return infraapp.ApprovalGrant{}, err
	}
	if err = tx.Commit(); err != nil {
		return infraapp.ApprovalGrant{}, err
	}
	return grant, nil
}

func (s *Store) AuthorizeApply(ctx context.Context, match infraapp.ApplyMatch) (infraapp.PlanRecord, error) {
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return infraapp.PlanRecord{}, err
	}
	defer func() { _ = tx.Rollback() }()
	a := match.Authorization
	plan, err := scanPlan(tx.QueryRowContext(ctx, `SELECT `+planColumns+` FROM infrastructure.plans WHERE id=$1 AND tenant_id=$2 AND project_id=$3 FOR UPDATE`, a.PlanID, match.TenantID, match.ProjectID))
	if err != nil {
		return infraapp.PlanRecord{}, mapNotFound(err)
	}
	if !plan.ApplyStartedAt.IsZero() {
		return infraapp.PlanRecord{}, infraapp.ErrConflict
	}
	if plan.Summary.PlanHash != a.PlanHash || plan.Estimate.Version != a.EstimateVersion || plan.Reservation.ReservationID != a.ReservationID || plan.Target != a.Target || !plan.Reservation.ExpiresAt.After(match.Now) || plan.Reservation.PlanHash != a.PlanHash {
		return infraapp.PlanRecord{}, infraapp.ErrPermissionDenied
	}
	if match.ApprovalRequired {
		result, updateErr := tx.ExecContext(ctx, `UPDATE infrastructure.approval_grants SET consumed_at=$1 WHERE id=$2 AND tenant_id=$3 AND project_id=$4 AND plan_id=$5 AND plan_hash=$6 AND estimate_version=$7 AND reservation_id=$8 AND target=$9 AND actor_id=$10 AND consumed_at IS NULL AND expires_at>$1`,
			match.Now, a.ApprovalGrantID, match.TenantID, match.ProjectID, a.PlanID, a.PlanHash,
			a.EstimateVersion, a.ReservationID, a.Target, a.ActorID)
		if updateErr != nil {
			return infraapp.PlanRecord{}, updateErr
		}
		if rows, rowsErr := result.RowsAffected(); rowsErr != nil || rows != 1 {
			return infraapp.PlanRecord{}, infraapp.ErrPermissionDenied
		}
	}
	plan.ApplyStartedAt = match.Now
	plan.Version++
	result, err := tx.ExecContext(ctx, `UPDATE infrastructure.plans SET apply_started_at=$1,version=$2 WHERE id=$3 AND version=$4`, match.Now, plan.Version, plan.Summary.PlanID, plan.Version-1)
	if err != nil {
		return infraapp.PlanRecord{}, err
	}
	if rows, rowsErr := result.RowsAffected(); rowsErr != nil || rows != 1 {
		return infraapp.PlanRecord{}, infraapp.ErrConflict
	}
	if err = audit(ctx, tx, match.TenantID, match.ProjectID, a.ActorID, "infrastructure.apply.authorized", a.PlanID, map[string]any{"plan_hash": a.PlanHash}, match.Now); err != nil {
		return infraapp.PlanRecord{}, err
	}
	if err = tx.Commit(); err != nil {
		return infraapp.PlanRecord{}, err
	}
	return plan, nil
}

type rowScanner interface{ Scan(...any) error }

func scanPlan(row rowScanner) (infraapp.PlanRecord, error) {
	var record infraapp.PlanRecord
	var changesRaw, estimateRaw, reservationRaw []byte
	var applyStarted sql.NullTime
	err := row.Scan(
		&record.Summary.PlanID, &record.TenantID, &record.Summary.ProjectID, &record.Summary.WorkspaceID,
		&record.Summary.SourceSHA, &record.Summary.PlanHash, &record.Summary.StateGeneration, &changesRaw,
		&record.Summary.Destructive, &record.Summary.RequiresApproval, &record.Summary.EstimateVersion,
		&record.Summary.EstimateFingerprint, &record.ArtifactDigest, &record.Target, &record.IdempotencyKey,
		&record.IdempotencyFingerprint, &estimateRaw, &reservationRaw, &applyStarted,
		&record.Summary.CreatedAt, &record.Version,
	)
	if err != nil {
		return infraapp.PlanRecord{}, err
	}
	if err = json.Unmarshal(changesRaw, &record.Summary.Changes); err != nil {
		return infraapp.PlanRecord{}, err
	}
	if err = json.Unmarshal(estimateRaw, &record.Estimate); err != nil {
		return infraapp.PlanRecord{}, err
	}
	if err = json.Unmarshal(reservationRaw, &record.Reservation); err != nil {
		return infraapp.PlanRecord{}, err
	}
	if applyStarted.Valid {
		record.ApplyStartedAt = applyStarted.Time
	}
	return record, nil
}

func marshalPlan(record infraapp.PlanRecord) ([]byte, []byte, []byte, error) {
	changes, err := json.Marshal(record.Summary.Changes)
	if err != nil {
		return nil, nil, nil, err
	}
	estimate, err := json.Marshal(record.Estimate)
	if err != nil {
		return nil, nil, nil, err
	}
	reservation, err := json.Marshal(record.Reservation)
	return changes, estimate, reservation, err
}

func audit(ctx context.Context, tx *sql.Tx, tenantID, projectID, actorID, action, resourceID string, metadata any, at time.Time) error {
	raw, err := json.Marshal(metadata)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO infrastructure.audit(tenant_id,project_id,actor_id,action,resource_id,metadata,occurred_at) VALUES($1,$2,$3,$4,$5,$6,$7)`, tenantID, projectID, actorID, action, resourceID, raw, at)
	return err
}

func nullableTime(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return value
}

func mapNotFound(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return infraapp.ErrNotFound
	}
	return err
}

func mapConflict(err error) error {
	value := strings.ToLower(fmt.Sprint(err))
	if strings.Contains(value, "23505") || strings.Contains(value, "40001") || strings.Contains(value, "40p01") || strings.Contains(value, "serialization") || strings.Contains(value, "deadlock") || strings.Contains(value, "duplicate key") || strings.Contains(value, "unique constraint") {
		return infraapp.ErrConflict
	}
	return err
}

var _ infraapp.Store = (*Store)(nil)
