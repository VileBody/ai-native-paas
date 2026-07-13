// Package postgres implements the Attachments application store with
// serializable PostgreSQL transactions and checksummed embedded migrations.
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

	"github.com/keir-research/ai-native-paas/internal/attachments/application"
	"github.com/keir-research/ai-native-paas/internal/attachments/domain"
	attachmentsv1 "github.com/keir-research/ai-native-paas/pkg/contracts/attachments/v1"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

type Store struct {
	DB                     *sql.DB
	MaxSerializableRetries int
}

var _ application.Store = (*Store)(nil)

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
	if _, err := s.DB.ExecContext(ctx, `CREATE SCHEMA IF NOT EXISTS attachments`); err != nil {
		return err
	}
	if _, err := s.DB.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS attachments.schema_migrations(version text PRIMARY KEY,checksum text NOT NULL,applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
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
		scanErr := s.DB.QueryRowContext(ctx, `SELECT checksum FROM attachments.schema_migrations WHERE version=$1`, entry.Name()).Scan(&existing)
		if scanErr == nil {
			if existing != checksum {
				return fmt.Errorf("attachments migration checksum mismatch: %s", entry.Name())
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
			_, err = tx.ExecContext(ctx, `INSERT INTO attachments.schema_migrations(version,checksum) VALUES($1,$2)`, entry.Name(), checksum)
		}
		if err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("apply attachments migration %s: %w", entry.Name(), err)
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
	if fn == nil {
		return domain.NewError(domain.CodeInvalidArgument, "transaction callback required")
	}
	attempts := s.MaxSerializableRetries
	if attempts <= 0 {
		attempts = 8
	}
	var last error
	for attempt := 0; attempt < attempts; attempt++ {
		tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
		if err != nil {
			return domain.Wrap(domain.CodeUnavailable, "begin attachments transaction", err)
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

func errorChain(err error) string {
	parts := []string{}
	for err != nil {
		parts = append(parts, strings.ToLower(err.Error()))
		err = errors.Unwrap(err)
	}
	return strings.Join(parts, " | ")
}
func retryableDB(err error) bool {
	v := errorChain(err)
	return strings.Contains(v, "40001") || strings.Contains(v, "40p01") || strings.Contains(v, "serialization") || strings.Contains(v, "deadlock") || strings.Contains(v, "23505") || strings.Contains(v, "duplicate key")
}
func mapDB(err error) error {
	if err == nil {
		return nil
	}
	v := errorChain(err)
	switch {
	case strings.Contains(v, "23505") || strings.Contains(v, "duplicate key") || strings.Contains(v, "unique constraint"):
		return domain.Wrap(domain.CodeConflict, "attachments unique constraint violated", err)
	case strings.Contains(v, "55000") || strings.Contains(v, "append-only") || strings.Contains(v, "immutable"):
		return domain.Wrap(domain.CodeConflict, "immutable attachments record", err)
	case strings.Contains(v, "23514") || strings.Contains(v, "check constraint") || strings.Contains(v, "payload drift"):
		return domain.Wrap(domain.CodeInvalidArgument, "attachments database constraint violated", err)
	default:
		return domain.Wrap(domain.CodeUnavailable, "attachments database operation failed", err)
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

type txAdapter struct {
	tx  *sql.Tx
	err error
}

var _ application.Tx = (*txAdapter)(nil)

func (a *txAdapter) capture(err error) {
	if err != nil && a.err == nil {
		a.err = err
	}
}

func (a *txAdapter) GetSecretSet(id string) (domain.SecretSet, bool) {
	v, err := scanPayload[domain.SecretSet](a.tx.QueryRow(`SELECT payload FROM attachments.secret_sets WHERE id=$1`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return v, false
	}
	a.capture(err)
	return v, err == nil
}
func (a *txAdapter) FindSecretSet(tenant, env string) (domain.SecretSet, bool) {
	v, err := scanPayload[domain.SecretSet](a.tx.QueryRow(`SELECT payload FROM attachments.secret_sets WHERE tenant_id=$1 AND environment_id=$2`, tenant, env))
	if errors.Is(err, sql.ErrNoRows) {
		return v, false
	}
	a.capture(err)
	return v, err == nil
}
func (a *txAdapter) PutSecretSet(v domain.SecretSet, expected int64) error {
	raw, err := marshal(v)
	if err != nil {
		return err
	}
	if expected == 0 {
		_, err = a.tx.Exec(`INSERT INTO attachments.secret_sets(id,tenant_id,application_id,environment_id,provider_path,version,payload,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, v.ID, v.TenantID, v.ApplicationID, v.EnvironmentID, v.ProviderPath, v.Version, raw, v.CreatedAt, v.UpdatedAt)
		return mapDB(err)
	}
	res, err := a.tx.Exec(`UPDATE attachments.secret_sets SET version=$1,payload=$2,updated_at=$3 WHERE id=$4 AND version=$5`, v.Version, raw, v.UpdatedAt, v.ID, expected)
	return affected(res, err, "secret set")
}

func (a *txAdapter) GetSecret(id string) (domain.SecretMetadata, bool) {
	v, err := scanPayload[domain.SecretMetadata](a.tx.QueryRow(`SELECT payload FROM attachments.secrets WHERE id=$1`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return v, false
	}
	a.capture(err)
	return v, err == nil
}
func (a *txAdapter) FindSecret(set, name string, scope attachmentsv1.SecretScope) (domain.SecretMetadata, bool) {
	v, err := scanPayload[domain.SecretMetadata](a.tx.QueryRow(`SELECT payload FROM attachments.secrets WHERE secret_set_id=$1 AND name=$2 AND scope=$3`, set, name, string(scope)))
	if errors.Is(err, sql.ErrNoRows) {
		return v, false
	}
	a.capture(err)
	return v, err == nil
}
func (a *txAdapter) ListSecrets(set string) []domain.SecretMetadata {
	rows, err := a.tx.Query(`SELECT payload FROM attachments.secrets WHERE secret_set_id=$1 ORDER BY name,id`, set)
	if err != nil {
		a.capture(err)
		return nil
	}
	defer rows.Close()
	out := []domain.SecretMetadata{}
	for rows.Next() {
		v, e := scanPayload[domain.SecretMetadata](rows)
		if e != nil {
			a.capture(e)
			return nil
		}
		out = append(out, v)
	}
	a.capture(rows.Err())
	return out
}
func nullableTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UTC()
}
func (a *txAdapter) PutSecret(v domain.SecretMetadata, expected int64) error {
	raw, err := marshal(v)
	if err != nil {
		return err
	}
	if expected == 0 {
		_, err = a.tx.Exec(`INSERT INTO attachments.secrets(id,tenant_id,secret_set_id,application_id,environment_id,name,scope,phase,provider_ref,provider_version,version,expires_at,deleted,payload,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)`, v.ID, v.TenantID, v.SecretSetID, v.ApplicationID, v.EnvironmentID, v.Name, string(v.Scope), string(v.Phase), v.ProviderRef, v.ProviderVersion, v.Version, nullableTime(v.ExpiresAt), v.Deleted, raw, v.CreatedAt, v.UpdatedAt)
		return mapDB(err)
	}
	res, err := a.tx.Exec(`UPDATE attachments.secrets SET provider_ref=$1,provider_version=$2,version=$3,expires_at=$4,deleted=$5,payload=$6,updated_at=$7 WHERE id=$8 AND version=$9`, v.ProviderRef, v.ProviderVersion, v.Version, nullableTime(v.ExpiresAt), v.Deleted, raw, v.UpdatedAt, v.ID, expected)
	return affected(res, err, "secret")
}

func (a *txAdapter) GetPlan(id string, version int64) (domain.ServicePlan, bool) {
	v, err := scanPayload[domain.ServicePlan](a.tx.QueryRow(`SELECT payload FROM attachments.service_plans WHERE id=$1 AND version=$2`, id, version))
	if errors.Is(err, sql.ErrNoRows) {
		return v, false
	}
	a.capture(err)
	return v, err == nil
}
func (a *txAdapter) LatestPlan(id string) (domain.ServicePlan, bool) {
	v, err := scanPayload[domain.ServicePlan](a.tx.QueryRow(`SELECT payload FROM attachments.service_plans WHERE id=$1 ORDER BY version DESC LIMIT 1`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return v, false
	}
	a.capture(err)
	return v, err == nil
}
func (a *txAdapter) ListPlans() []domain.ServicePlan {
	rows, err := a.tx.Query(`SELECT payload FROM attachments.service_plans ORDER BY id,version`)
	if err != nil {
		a.capture(err)
		return nil
	}
	defer rows.Close()
	out := []domain.ServicePlan{}
	for rows.Next() {
		v, e := scanPayload[domain.ServicePlan](rows)
		if e != nil {
			a.capture(e)
			return nil
		}
		out = append(out, v)
	}
	a.capture(rows.Err())
	return out
}
func (a *txAdapter) PutPlan(v domain.ServicePlan) error {
	raw, err := marshal(v)
	if err != nil {
		return err
	}
	result, err := a.tx.Exec(`INSERT INTO attachments.service_plans(id,version,service_type,provider,provider_plan,provider_mapping_version,enabled,payload,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9) ON CONFLICT (id,version) DO NOTHING`, v.ID, v.Version, string(v.Type), v.Provider, v.ProviderPlan, v.ProviderMappingVersion, v.Enabled, raw, v.CreatedAt)
	if err != nil {
		return mapDB(err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 1 {
		return nil
	}
	old, err := scanPayload[domain.ServicePlan](a.tx.QueryRow(`SELECT payload FROM attachments.service_plans WHERE id=$1 AND version=$2`, v.ID, v.Version))
	if err != nil {
		return mapDB(err)
	}
	if domain.Hash(old) == domain.Hash(v) {
		return nil
	}
	return domain.NewError(domain.CodeConflict, "service plan version is immutable")
}

func (a *txAdapter) GetInstance(id string) (domain.ServiceInstance, bool) {
	v, err := scanPayload[domain.ServiceInstance](a.tx.QueryRow(`SELECT payload FROM attachments.service_instances WHERE id=$1`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return v, false
	}
	a.capture(err)
	return v, err == nil
}
func (a *txAdapter) FindInstance(tenant, env, name string) (domain.ServiceInstance, bool) {
	v, err := scanPayload[domain.ServiceInstance](a.tx.QueryRow(`SELECT payload FROM attachments.service_instances WHERE tenant_id=$1 AND environment_id=$2 AND lower(name)=lower($3) AND state<>'DELETED' ORDER BY created_at LIMIT 1`, tenant, env, name))
	if errors.Is(err, sql.ErrNoRows) {
		return v, false
	}
	a.capture(err)
	return v, err == nil
}
func (a *txAdapter) ListInstances(tenant, app string) []domain.ServiceInstance {
	rows, err := a.tx.Query(`SELECT payload FROM attachments.service_instances WHERE tenant_id=$1 AND application_id=$2 ORDER BY created_at,id`, tenant, app)
	if err != nil {
		a.capture(err)
		return nil
	}
	defer rows.Close()
	out := []domain.ServiceInstance{}
	for rows.Next() {
		v, e := scanPayload[domain.ServiceInstance](rows)
		if e != nil {
			a.capture(e)
			return nil
		}
		out = append(out, v)
	}
	a.capture(rows.Err())
	return out
}
func (a *txAdapter) PutInstance(v domain.ServiceInstance, expected int64) error {
	raw, err := marshal(v)
	if err != nil {
		return err
	}
	if expected == 0 {
		_, err = a.tx.Exec(`INSERT INTO attachments.service_instances(id,tenant_id,application_id,environment_id,name,plan_id,plan_version,service_type,state,provider_operation_key,provider_id,provider_endpoint,version,payload,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)`, v.ID, v.TenantID, v.ApplicationID, v.EnvironmentID, v.Name, v.PlanID, v.PlanVersion, string(v.Type), string(v.State), v.ProviderOperationKey, v.ProviderID, v.ProviderEndpoint, v.Version, raw, v.CreatedAt, v.UpdatedAt)
		return mapDB(err)
	}
	res, err := a.tx.Exec(`UPDATE attachments.service_instances SET state=$1,provider_id=$2,provider_endpoint=$3,version=$4,payload=$5,updated_at=$6 WHERE id=$7 AND version=$8`, string(v.State), v.ProviderID, v.ProviderEndpoint, v.Version, raw, v.UpdatedAt, v.ID, expected)
	return affected(res, err, "service instance")
}

func (a *txAdapter) GetBinding(id string) (domain.ServiceBinding, bool) {
	v, err := scanPayload[domain.ServiceBinding](a.tx.QueryRow(`SELECT payload FROM attachments.service_bindings WHERE id=$1`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return v, false
	}
	a.capture(err)
	return v, err == nil
}
func (a *txAdapter) FindBinding(tenant, env, instance string) (domain.ServiceBinding, bool) {
	v, err := scanPayload[domain.ServiceBinding](a.tx.QueryRow(`SELECT payload FROM attachments.service_bindings WHERE tenant_id=$1 AND environment_id=$2 AND instance_id=$3 AND state<>'REVOKED' ORDER BY created_at LIMIT 1`, tenant, env, instance))
	if errors.Is(err, sql.ErrNoRows) {
		return v, false
	}
	a.capture(err)
	return v, err == nil
}
func (a *txAdapter) listBindings(query string, args ...any) []domain.ServiceBinding {
	rows, err := a.tx.Query(query, args...)
	if err != nil {
		a.capture(err)
		return nil
	}
	defer rows.Close()
	out := []domain.ServiceBinding{}
	for rows.Next() {
		v, e := scanPayload[domain.ServiceBinding](rows)
		if e != nil {
			a.capture(e)
			return nil
		}
		out = append(out, v)
	}
	a.capture(rows.Err())
	return out
}
func (a *txAdapter) ListBindings(tenant, env string) []domain.ServiceBinding {
	return a.listBindings(`SELECT payload FROM attachments.service_bindings WHERE tenant_id=$1 AND environment_id=$2 ORDER BY id`, tenant, env)
}
func (a *txAdapter) ListAppBindings(tenant, app string) []domain.ServiceBinding {
	return a.listBindings(`SELECT payload FROM attachments.service_bindings WHERE tenant_id=$1 AND application_id=$2 ORDER BY id`, tenant, app)
}
func (a *txAdapter) PutBinding(v domain.ServiceBinding, expected int64) error {
	raw, err := marshal(v)
	if err != nil {
		return err
	}
	if expected == 0 {
		_, err = a.tx.Exec(`INSERT INTO attachments.service_bindings(id,tenant_id,application_id,environment_id,instance_id,state,provider_credential_id,version,payload,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, v.ID, v.TenantID, v.ApplicationID, v.EnvironmentID, v.InstanceID, string(v.State), v.ProviderCredentialID, v.Version, raw, v.CreatedAt, v.UpdatedAt)
		return mapDB(err)
	}
	res, err := a.tx.Exec(`UPDATE attachments.service_bindings SET state=$1,provider_credential_id=$2,version=$3,payload=$4,updated_at=$5 WHERE id=$6 AND version=$7`, string(v.State), v.ProviderCredentialID, v.Version, raw, v.UpdatedAt, v.ID, expected)
	return affected(res, err, "service binding")
}

func (a *txAdapter) GetClaim(id string) (domain.DomainClaim, bool) {
	v, err := scanPayload[domain.DomainClaim](a.tx.QueryRow(`SELECT payload FROM attachments.domain_claims WHERE id=$1`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return v, false
	}
	a.capture(err)
	return v, err == nil
}
func (a *txAdapter) FindClaim(host string) (domain.DomainClaim, bool) {
	v, err := scanPayload[domain.DomainClaim](a.tx.QueryRow(`SELECT payload FROM attachments.domain_claims WHERE lower(hostname)=lower($1) AND state<>'RELEASED' ORDER BY created_at LIMIT 1`, host))
	if errors.Is(err, sql.ErrNoRows) {
		return v, false
	}
	a.capture(err)
	return v, err == nil
}
func (a *txAdapter) ListClaims(tenant, env string) []domain.DomainClaim {
	rows, err := a.tx.Query(`SELECT payload FROM attachments.domain_claims WHERE tenant_id=$1 AND environment_id=$2 ORDER BY hostname,id`, tenant, env)
	if err != nil {
		a.capture(err)
		return nil
	}
	defer rows.Close()
	out := []domain.DomainClaim{}
	for rows.Next() {
		v, e := scanPayload[domain.DomainClaim](rows)
		if e != nil {
			a.capture(e)
			return nil
		}
		out = append(out, v)
	}
	a.capture(rows.Err())
	return out
}
func (a *txAdapter) PutClaim(v domain.DomainClaim, expected int64) error {
	raw, err := marshal(v)
	if err != nil {
		return err
	}
	if expected == 0 {
		_, err = a.tx.Exec(`INSERT INTO attachments.domain_claims(id,tenant_id,application_id,environment_id,hostname,generated,state,challenge_name,challenge_value,version,payload,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`, v.ID, v.TenantID, v.ApplicationID, v.EnvironmentID, v.Hostname, v.Generated, string(v.State), v.ChallengeName, v.ChallengeValue, v.Version, raw, v.CreatedAt, v.UpdatedAt)
		return mapDB(err)
	}
	res, err := a.tx.Exec(`UPDATE attachments.domain_claims SET state=$1,version=$2,payload=$3,updated_at=$4 WHERE id=$5 AND version=$6`, string(v.State), v.Version, raw, v.UpdatedAt, v.ID, expected)
	return affected(res, err, "domain claim")
}

func (a *txAdapter) GetSnapshot(id string) (domain.AttachmentSnapshot, bool) {
	v, err := scanPayload[domain.AttachmentSnapshot](a.tx.QueryRow(`SELECT payload FROM attachments.snapshots WHERE id=$1`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return v, false
	}
	a.capture(err)
	return v, err == nil
}
func (a *txAdapter) LatestSnapshot(tenant, env string) (domain.AttachmentSnapshot, bool) {
	v, err := scanPayload[domain.AttachmentSnapshot](a.tx.QueryRow(`SELECT payload FROM attachments.snapshots WHERE tenant_id=$1 AND environment_id=$2 ORDER BY version DESC LIMIT 1`, tenant, env))
	if errors.Is(err, sql.ErrNoRows) {
		return v, false
	}
	a.capture(err)
	return v, err == nil
}
func (a *txAdapter) FindSnapshot(tenant, env, hash string) (domain.AttachmentSnapshot, bool) {
	v, err := scanPayload[domain.AttachmentSnapshot](a.tx.QueryRow(`SELECT payload FROM attachments.snapshots WHERE tenant_id=$1 AND environment_id=$2 AND content_hash=$3`, tenant, env, hash))
	if errors.Is(err, sql.ErrNoRows) {
		return v, false
	}
	a.capture(err)
	return v, err == nil
}
func (a *txAdapter) PutSnapshot(v domain.AttachmentSnapshot) error {
	raw, err := marshal(v)
	if err != nil {
		return err
	}
	_, err = a.tx.Exec(`INSERT INTO attachments.snapshots(id,tenant_id,application_id,environment_id,version,content_hash,secret_set_ref,payload,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, v.Value.SnapshotID, v.Value.TenantID, v.Value.ApplicationID, v.Value.EnvironmentID, v.Value.Version, v.ContentHash, v.Value.SecretSetRef, raw, v.Value.CreatedAt)
	return mapDB(err)
}

func (a *txAdapter) GetIdempotency(tenant, scope, key string) (domain.IdempotencyRecord, bool) {
	var v domain.IdempotencyRecord
	err := a.tx.QueryRow(`SELECT tenant_id,scope,key,request_hash,resource_id,created_at FROM attachments.idempotency WHERE tenant_id=$1 AND scope=$2 AND key=$3`, tenant, scope, key).Scan(&v.TenantID, &v.Scope, &v.Key, &v.RequestHash, &v.ResourceID, &v.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return v, false
	}
	a.capture(err)
	return v, err == nil
}
func (a *txAdapter) PutIdempotency(v domain.IdempotencyRecord) error {
	result, err := a.tx.Exec(`INSERT INTO attachments.idempotency(tenant_id,scope,key,request_hash,resource_id,created_at) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT (tenant_id,scope,key) DO NOTHING`, v.TenantID, v.Scope, v.Key, v.RequestHash, v.ResourceID, v.CreatedAt)
	if err != nil {
		return mapDB(err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 1 {
		return nil
	}
	var hash, id string
	if err := a.tx.QueryRow(`SELECT request_hash,resource_id FROM attachments.idempotency WHERE tenant_id=$1 AND scope=$2 AND key=$3`, v.TenantID, v.Scope, v.Key).Scan(&hash, &id); err != nil {
		return mapDB(err)
	}
	if hash == v.RequestHash && id == v.ResourceID {
		return nil
	}
	return domain.NewError(domain.CodeConflict, "idempotency key reused with a different request")
}
func validJSON(raw []byte) []byte {
	if json.Valid(raw) {
		return raw
	}
	return []byte(`{}`)
}
func (a *txAdapter) AppendOutbox(v domain.OutboxRecord) error {
	_, err := a.tx.Exec(`INSERT INTO attachments.outbox(id,tenant_id,topic,aggregate_id,payload,created_at) VALUES($1,$2,$3,$4,$5,$6)`, v.ID, v.TenantID, v.Topic, v.AggregateID, validJSON(v.Payload), v.CreatedAt)
	return mapDB(err)
}
func (a *txAdapter) AppendAudit(v domain.AuditRecord) error {
	_, err := a.tx.Exec(`INSERT INTO attachments.audit(id,tenant_id,actor_id,action,resource_type,resource_id,data,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, v.ID, v.TenantID, v.ActorID, v.Action, v.ResourceType, v.ResourceID, validJSON(v.Data), v.CreatedAt)
	return mapDB(err)
}
