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

	"github.com/keir-research/ai-native-paas/internal/commerce/application"
	"github.com/keir-research/ai-native-paas/internal/commerce/domain"
	commercev1 "github.com/keir-research/ai-native-paas/pkg/contracts/commerce/v1"
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
	if _, err := s.DB.ExecContext(ctx, `CREATE SCHEMA IF NOT EXISTS commerce`); err != nil {
		return err
	}
	if _, err := s.DB.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS commerce.schema_migrations(version text PRIMARY KEY,checksum text NOT NULL,applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
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
		scanErr := s.DB.QueryRowContext(ctx, `SELECT checksum FROM commerce.schema_migrations WHERE version=$1`, entry.Name()).Scan(&existing)
		if scanErr == nil {
			if existing != checksum {
				return fmt.Errorf("commerce migration checksum mismatch: %s", entry.Name())
			}
			continue
		}
		if !errors.Is(scanErr, sql.ErrNoRows) {
			return scanErr
		}
		tx, err := s.DB.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, string(raw)); err == nil {
			_, err = tx.ExecContext(ctx, `INSERT INTO commerce.schema_migrations(version,checksum) VALUES($1,$2)`, entry.Name(), checksum)
		}
		if err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("apply commerce migration %s: %w", entry.Name(), err)
		}
		if err = tx.Commit(); err != nil {
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
			return domain.Wrap(domain.CodeUnavailable, "begin commerce transaction", err)
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
	v := strings.ToLower(fmt.Sprint(err))
	return strings.Contains(v, "40001") || strings.Contains(v, "40p01") || strings.Contains(v, "serialization") || strings.Contains(v, "deadlock") || strings.Contains(v, "23505") || strings.Contains(v, "duplicate key")
}

type txAdapter struct {
	tx  *sql.Tx
	err error
}

func (a *txAdapter) capture(err error) {
	if err != nil && a.err == nil {
		a.err = err
	}
}
func marshal(v any) ([]byte, error)       { return json.Marshal(v) }
func decode[T any](raw []byte) (T, error) { var v T; err := json.Unmarshal(raw, &v); return v, err }
func scanPayload[T any](row interface{ Scan(...any) error }) (T, error) {
	var raw []byte
	if err := row.Scan(&raw); err != nil {
		var zero T
		return zero, err
	}
	return decode[T](raw)
}
func mapDB(err error) error {
	if err == nil {
		return nil
	}
	v := strings.ToLower(fmt.Sprint(err))
	switch {
	case strings.Contains(v, "23505") || strings.Contains(v, "duplicate key") || strings.Contains(v, "unique constraint"):
		return domain.Wrap(domain.CodeConflict, "commerce unique constraint violated", err)
	case strings.Contains(v, "55000") || strings.Contains(v, "append-only") || strings.Contains(v, "immutable"):
		return domain.Wrap(domain.CodeConflict, "immutable commerce record", err)
	case strings.Contains(v, "23514") || strings.Contains(v, "check constraint") || strings.Contains(v, "payload drift"):
		return domain.Wrap(domain.CodeInvalidArgument, "commerce database constraint violated", err)
	default:
		return domain.Wrap(domain.CodeUnavailable, "commerce database operation failed", err)
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
		return domain.NewError(domain.CodeStaleVersion, name+" version is stale")
	}
	return nil
}
func hashSpec(spec commercev1.PlanSpec) (string, error) {
	raw, err := json.Marshal(spec)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func (a *txAdapter) GetPlanDefinition(id string) (domain.PlanDefinition, bool) {
	v, err := scanPayload[domain.PlanDefinition](a.tx.QueryRow(`SELECT payload FROM commerce.plan_definitions WHERE id=$1`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return v, false
	}
	a.capture(err)
	return v, err == nil
}
func (a *txAdapter) InsertPlanDefinition(v domain.PlanDefinition) error {
	raw, err := marshal(v)
	if err != nil {
		return err
	}
	_, err = a.tx.Exec(`INSERT INTO commerce.plan_definitions(id,name,version,payload,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6)`, v.ID, v.Name, v.Version, raw, v.CreatedAt, v.UpdatedAt)
	return mapDB(err)
}
func (a *txAdapter) UpdatePlanDefinition(v domain.PlanDefinition, expected int64) error {
	raw, err := marshal(v)
	if err != nil {
		return err
	}
	res, err := a.tx.Exec(`UPDATE commerce.plan_definitions SET name=$1,version=$2,payload=$3,updated_at=$4 WHERE id=$5 AND version=$6`, v.Name, v.Version, raw, v.UpdatedAt, v.ID, expected)
	return affected(res, err, "plan definition")
}

func (a *txAdapter) GetPlanVersion(id string) (domain.PlanVersion, bool) {
	v, err := scanPayload[domain.PlanVersion](a.tx.QueryRow(`SELECT payload FROM commerce.plan_versions WHERE id=$1`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return v, false
	}
	a.capture(err)
	return v, err == nil
}
func (a *txAdapter) ListPlanVersions(definition string) []domain.PlanVersion {
	rows, err := a.tx.Query(`SELECT payload FROM commerce.plan_versions WHERE definition_id=$1 ORDER BY number,id`, definition)
	if err != nil {
		a.capture(err)
		return nil
	}
	defer rows.Close()
	out := []domain.PlanVersion{}
	for rows.Next() {
		v, err := scanPayload[domain.PlanVersion](rows)
		if err != nil {
			a.capture(err)
			return nil
		}
		out = append(out, v)
	}
	a.capture(rows.Err())
	return out
}
func (a *txAdapter) InsertPlanVersion(v domain.PlanVersion) error {
	raw, err := marshal(v)
	if err != nil {
		return err
	}
	hash, err := hashSpec(v.Spec)
	if err != nil {
		return err
	}
	_, err = a.tx.Exec(`INSERT INTO commerce.plan_versions(id,definition_id,policy_version,number,state,effective_from,currency,spec_hash,version,payload,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`, v.ID, v.DefinitionID, v.PolicyVersion, v.Number, string(v.State), v.EffectiveFrom, v.Spec.Currency, hash, v.Version, raw, v.CreatedAt, v.UpdatedAt)
	return mapDB(err)
}
func (a *txAdapter) UpdatePlanVersion(v domain.PlanVersion, expected int64) error {
	raw, err := marshal(v)
	if err != nil {
		return err
	}
	hash, err := hashSpec(v.Spec)
	if err != nil {
		return err
	}
	res, err := a.tx.Exec(`UPDATE commerce.plan_versions SET state=$1,currency=$2,spec_hash=$3,version=$4,payload=$5,updated_at=$6 WHERE id=$7 AND version=$8`, string(v.State), v.Spec.Currency, hash, v.Version, raw, v.UpdatedAt, v.ID, expected)
	return affected(res, err, "plan version")
}

func (a *txAdapter) GetSubscription(id string) (domain.Subscription, bool) {
	v, err := scanPayload[domain.Subscription](a.tx.QueryRow(`SELECT payload FROM commerce.subscriptions WHERE id=$1`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return v, false
	}
	a.capture(err)
	return v, err == nil
}
func (a *txAdapter) FindSubscriptionByTenant(tenant string) (domain.Subscription, bool) {
	v, err := scanPayload[domain.Subscription](a.tx.QueryRow(`SELECT payload FROM commerce.subscriptions WHERE tenant_id=$1 ORDER BY created_at DESC,id DESC LIMIT 1`, tenant))
	if errors.Is(err, sql.ErrNoRows) {
		return v, false
	}
	a.capture(err)
	return v, err == nil
}
func (a *txAdapter) InsertSubscription(v domain.Subscription) error {
	raw, err := marshal(v)
	if err != nil {
		return err
	}
	var trial any
	if !v.TrialEndsAt.IsZero() {
		trial = v.TrialEndsAt
	}
	_, err = a.tx.Exec(`INSERT INTO commerce.subscriptions(id,tenant_id,plan_version_id,state,trial_ends_at,version,payload,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, v.ID, v.TenantID, v.PlanVersionID, string(v.State), trial, v.Version, raw, v.CreatedAt, v.UpdatedAt)
	return mapDB(err)
}
func (a *txAdapter) UpdateSubscription(v domain.Subscription, expected int64) error {
	raw, err := marshal(v)
	if err != nil {
		return err
	}
	var trial any
	if !v.TrialEndsAt.IsZero() {
		trial = v.TrialEndsAt
	}
	res, err := a.tx.Exec(`UPDATE commerce.subscriptions SET state=$1,trial_ends_at=$2,version=$3,payload=$4,updated_at=$5 WHERE id=$6 AND version=$7`, string(v.State), trial, v.Version, raw, v.UpdatedAt, v.ID, expected)
	return affected(res, err, "subscription")
}

func (a *txAdapter) GetBillingPeriod(id string) (domain.BillingPeriod, bool) {
	v, err := scanPayload[domain.BillingPeriod](a.tx.QueryRow(`SELECT payload FROM commerce.billing_periods WHERE id=$1`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return v, false
	}
	a.capture(err)
	return v, err == nil
}
func (a *txAdapter) ListBillingPeriods(tenant string) []domain.BillingPeriod {
	rows, err := a.tx.Query(`SELECT payload FROM commerce.billing_periods WHERE tenant_id=$1 ORDER BY start_at,id`, tenant)
	if err != nil {
		a.capture(err)
		return nil
	}
	defer rows.Close()
	out := []domain.BillingPeriod{}
	for rows.Next() {
		v, err := scanPayload[domain.BillingPeriod](rows)
		if err != nil {
			a.capture(err)
			return nil
		}
		out = append(out, v)
	}
	a.capture(rows.Err())
	return out
}
func (a *txAdapter) InsertBillingPeriod(v domain.BillingPeriod) error {
	if _, err := a.tx.Exec(`SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "period:"+v.SubscriptionID); err != nil {
		return mapDB(err)
	}
	var overlap int
	err := a.tx.QueryRow(`SELECT count(*) FROM commerce.billing_periods WHERE subscription_id=$1 AND $2 < end_at AND start_at < $3`, v.SubscriptionID, v.Start, v.End).Scan(&overlap)
	if err != nil {
		return mapDB(err)
	}
	if overlap > 0 {
		return domain.NewError(domain.CodeConflict, "billing periods overlap")
	}
	raw, err := marshal(v)
	if err != nil {
		return err
	}
	_, err = a.tx.Exec(`INSERT INTO commerce.billing_periods(id,tenant_id,subscription_id,plan_version_id,start_at,end_at,state,version,payload,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, v.ID, v.TenantID, v.SubscriptionID, v.PlanVersionID, v.Start, v.End, string(v.State), v.Version, raw, v.CreatedAt, v.UpdatedAt)
	return mapDB(err)
}
func (a *txAdapter) UpdateBillingPeriod(v domain.BillingPeriod, expected int64) error {
	raw, err := marshal(v)
	if err != nil {
		return err
	}
	res, err := a.tx.Exec(`UPDATE commerce.billing_periods SET state=$1,version=$2,payload=$3,updated_at=$4 WHERE id=$5 AND version=$6`, string(v.State), v.Version, raw, v.UpdatedAt, v.ID, expected)
	return affected(res, err, "billing period")
}

func (a *txAdapter) GetCommercialAccount(tenant string) (domain.CommercialAccount, bool) {
	v, err := scanPayload[domain.CommercialAccount](a.tx.QueryRow(`SELECT payload FROM commerce.commercial_accounts WHERE tenant_id=$1`, tenant))
	if errors.Is(err, sql.ErrNoRows) {
		return v, false
	}
	a.capture(err)
	return v, err == nil
}
func (a *txAdapter) InsertCommercialAccount(v domain.CommercialAccount) error {
	raw, err := marshal(v)
	if err != nil {
		return err
	}
	_, err = a.tx.Exec(`INSERT INTO commerce.commercial_accounts(tenant_id,state,version,payload,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6)`, v.TenantID, string(v.State), v.Version, raw, v.CreatedAt, v.UpdatedAt)
	return mapDB(err)
}
func (a *txAdapter) UpdateCommercialAccount(v domain.CommercialAccount, expected int64) error {
	raw, err := marshal(v)
	if err != nil {
		return err
	}
	res, err := a.tx.Exec(`UPDATE commerce.commercial_accounts SET state=$1,version=$2,payload=$3,updated_at=$4 WHERE tenant_id=$5 AND version=$6`, string(v.State), v.Version, raw, v.UpdatedAt, v.TenantID, expected)
	return affected(res, err, "commercial account")
}

func (a *txAdapter) GetQuotaReservation(id string) (domain.QuotaReservation, bool) {
	v, err := scanPayload[domain.QuotaReservation](a.tx.QueryRow(`SELECT payload FROM commerce.quota_reservations WHERE id=$1`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return v, false
	}
	a.capture(err)
	return v, err == nil
}
func (a *txAdapter) FindQuotaReservation(tenant, key string) (domain.QuotaReservation, bool) {
	v, err := scanPayload[domain.QuotaReservation](a.tx.QueryRow(`SELECT payload FROM commerce.quota_reservations WHERE tenant_id=$1 AND idempotency_key=$2`, tenant, key))
	if errors.Is(err, sql.ErrNoRows) {
		return v, false
	}
	a.capture(err)
	return v, err == nil
}
func (a *txAdapter) ListQuotaReservations(tenant, resource string) []domain.QuotaReservation {
	if resource != "" {
		if _, err := a.tx.Exec(`SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "quota:"+tenant+":"+resource); err != nil {
			a.capture(mapDB(err))
			return nil
		}
	}
	query := `SELECT payload FROM commerce.quota_reservations WHERE tenant_id=$1`
	args := []any{tenant}
	if resource != "" {
		query += ` AND resource=$2`
		args = append(args, resource)
	}
	query += ` ORDER BY created_at,id`
	rows, err := a.tx.Query(query, args...)
	if err != nil {
		a.capture(err)
		return nil
	}
	defer rows.Close()
	out := []domain.QuotaReservation{}
	for rows.Next() {
		v, err := scanPayload[domain.QuotaReservation](rows)
		if err != nil {
			a.capture(err)
			return nil
		}
		out = append(out, v)
	}
	a.capture(rows.Err())
	return out
}
func (a *txAdapter) InsertQuotaReservation(v domain.QuotaReservation) error {
	raw, err := marshal(v)
	if err != nil {
		return err
	}
	_, err = a.tx.Exec(`INSERT INTO commerce.quota_reservations(id,tenant_id,resource,policy_version,reason,quantity,state,idempotency_key,fingerprint,expires_at,version,payload,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)`, v.ID, v.TenantID, v.Resource, v.PolicyVersion, v.Reason, v.Quantity, string(v.State), v.IdempotencyKey, v.Fingerprint, v.ExpiresAt, v.Version, raw, v.CreatedAt, v.UpdatedAt)
	return mapDB(err)
}
func (a *txAdapter) UpdateQuotaReservation(v domain.QuotaReservation, expected int64) error {
	raw, err := marshal(v)
	if err != nil {
		return err
	}
	res, err := a.tx.Exec(`UPDATE commerce.quota_reservations SET reason=$1,state=$2,version=$3,payload=$4,updated_at=$5 WHERE id=$6 AND version=$7`, v.Reason, string(v.State), v.Version, raw, v.UpdatedAt, v.ID, expected)
	return affected(res, err, "quota reservation")
}

func (a *txAdapter) FindUsageByKey(tenant, key string) (commercev1.UsageEvent, bool) {
	v, err := scanPayload[commercev1.UsageEvent](a.tx.QueryRow(`SELECT payload FROM commerce.usage_events WHERE tenant_id=$1 AND idempotency_key=$2`, tenant, key))
	if errors.Is(err, sql.ErrNoRows) {
		return v, false
	}
	a.capture(err)
	return v, err == nil
}
func (a *txAdapter) ListUsage(tenant, period string) []commercev1.UsageEvent {
	query := `SELECT payload FROM commerce.usage_events WHERE tenant_id=$1`
	args := []any{tenant}
	if period != "" {
		query += ` AND period_id=$2`
		args = append(args, period)
	}
	query += ` ORDER BY occurred_at,id`
	rows, err := a.tx.Query(query, args...)
	if err != nil {
		a.capture(err)
		return nil
	}
	defer rows.Close()
	out := []commercev1.UsageEvent{}
	for rows.Next() {
		v, err := scanPayload[commercev1.UsageEvent](rows)
		if err != nil {
			a.capture(err)
			return nil
		}
		out = append(out, v)
	}
	a.capture(rows.Err())
	return out
}
func (a *txAdapter) InsertUsage(v commercev1.UsageEvent) error {
	raw, err := marshal(v)
	if err != nil {
		return err
	}
	meta, err := json.Marshal(v.Metadata)
	if err != nil {
		return err
	}
	_, err = a.tx.Exec(`INSERT INTO commerce.usage_events(id,tenant_id,period_id,resource_type,resource_id,meter,kind,quantity,idempotency_key,occurred_at,window_start,window_end,metadata,version,payload,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)`, v.ID, v.TenantID, v.PeriodID, v.ResourceType, v.ResourceID, string(v.Meter), string(v.Kind), v.Quantity, v.IdempotencyKey, v.OccurredAt, v.WindowStart, v.WindowEnd, meta, v.Version, raw, v.CreatedAt)
	return mapDB(err)
}

func (a *txAdapter) GetIdempotency(tenant, scope, key string) (application.IdempotencyRecord, bool) {
	var v application.IdempotencyRecord
	err := a.tx.QueryRow(`SELECT tenant_id,scope,key,fingerprint,resource_id,created_at FROM commerce.idempotency WHERE tenant_id=$1 AND scope=$2 AND key=$3`, tenant, scope, key).Scan(&v.TenantID, &v.Scope, &v.Key, &v.Fingerprint, &v.ResourceID, &v.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return v, false
	}
	a.capture(err)
	return v, err == nil
}
func (a *txAdapter) InsertIdempotency(v application.IdempotencyRecord) error {
	_, err := a.tx.Exec(`INSERT INTO commerce.idempotency(tenant_id,scope,key,fingerprint,resource_id,created_at) VALUES($1,$2,$3,$4,$5,$6)`, v.TenantID, v.Scope, v.Key, v.Fingerprint, v.ResourceID, v.CreatedAt)
	return mapDB(err)
}
func (a *txAdapter) AppendOutbox(v application.OutboxRecord) error {
	_, err := a.tx.Exec(`INSERT INTO commerce.outbox(id,tenant_id,topic,aggregate_id,payload,created_at) VALUES($1,$2,$3,$4,$5,$6)`, v.ID, v.TenantID, v.Topic, v.AggregateID, v.Payload, v.CreatedAt)
	return mapDB(err)
}
func (a *txAdapter) AppendAudit(v application.AuditRecord) error {
	_, err := a.tx.Exec(`INSERT INTO commerce.audit(id,tenant_id,actor_id,action,resource_type,resource_id,data,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, v.ID, v.TenantID, v.ActorID, v.Action, v.ResourceType, v.ResourceID, v.Data, v.CreatedAt)
	return mapDB(err)
}
func (a *txAdapter) AppendAlert(v application.ReconciliationAlert) error {
	_, err := a.tx.Exec(`INSERT INTO commerce.reconciliation_alerts(id,tenant_id,period_id,meter,resource_id,reason,drift,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, v.ID, v.TenantID, v.PeriodID, v.Meter, v.ResourceID, v.Reason, v.Drift, v.CreatedAt)
	return mapDB(err)
}
func (a *txAdapter) ListAlerts(tenant, period string) []application.ReconciliationAlert {
	query := `SELECT id,tenant_id,period_id,meter,resource_id,reason,drift,created_at FROM commerce.reconciliation_alerts WHERE tenant_id=$1`
	args := []any{tenant}
	if period != "" {
		query += ` AND period_id=$2`
		args = append(args, period)
	}
	query += ` ORDER BY created_at,id`
	rows, err := a.tx.Query(query, args...)
	if err != nil {
		a.capture(err)
		return nil
	}
	defer rows.Close()
	out := []application.ReconciliationAlert{}
	for rows.Next() {
		var v application.ReconciliationAlert
		if err := rows.Scan(&v.ID, &v.TenantID, &v.PeriodID, &v.Meter, &v.ResourceID, &v.Reason, &v.Drift, &v.CreatedAt); err != nil {
			a.capture(err)
			return nil
		}
		out = append(out, v)
	}
	a.capture(rows.Err())
	return out
}

var _ application.Store = (*Store)(nil)
var _ application.Tx = (*txAdapter)(nil)
