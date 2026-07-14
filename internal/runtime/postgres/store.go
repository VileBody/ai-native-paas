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
	"math/rand/v2"
	"sort"
	"strings"
	"time"

	"github.com/keir-research/ai-native-paas/internal/runtime/application"
	"github.com/keir-research/ai-native-paas/internal/runtime/domain"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

type Store struct {
	DB                     *sql.DB
	MaxSerializableRetries int
}

const defaultSerializableRetries = 32

func NewStore(db *sql.DB) (*Store, error) {
	if db == nil {
		return nil, errors.New("postgres db is nil")
	}
	return &Store{DB: db, MaxSerializableRetries: defaultSerializableRetries}, nil
}
func (s *Store) Migrate(ctx context.Context) error {
	if s == nil || s.DB == nil {
		return errors.New("postgres db is nil")
	}
	if _, err := s.DB.ExecContext(ctx, `CREATE SCHEMA IF NOT EXISTS runtime`); err != nil {
		return err
	}
	if _, err := s.DB.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS runtime.schema_migrations (version text PRIMARY KEY, checksum text NOT NULL, applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
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
		scanErr := s.DB.QueryRowContext(ctx, `SELECT checksum FROM runtime.schema_migrations WHERE version=$1`, entry.Name()).Scan(&existing)
		if scanErr == nil {
			if existing != checksum {
				return fmt.Errorf("runtime migration checksum mismatch: %s", entry.Name())
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
			_, err = tx.ExecContext(ctx, `INSERT INTO runtime.schema_migrations(version,checksum) VALUES($1,$2)`, entry.Name(), checksum)
		}
		if err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("apply runtime migration %s: %w", entry.Name(), err)
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
		attempts = defaultSerializableRetries
	}
	var last error
	for attempt := 0; attempt < attempts; attempt++ {
		tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
		if err != nil {
			return err
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
			return err
		} else {
			last = err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(serializableRetryDelay(attempt)):
		}
	}
	if domain.HasCode(last, domain.CodeConflict) {
		return last
	}
	return domain.Wrap(domain.CodeUnavailable, "serializable transaction retry budget exhausted", last)
}

func serializableRetryDelay(attempt int) time.Duration {
	shift := attempt
	if shift > 6 {
		shift = 6
	}
	ceiling := 5 * time.Millisecond * time.Duration(1<<shift)
	return ceiling/2 + time.Duration(rand.Int64N(int64(ceiling)))
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
		return domain.Wrap(domain.CodeConflict, "unique constraint violated", err)
	case strings.Contains(v, "55000"):
		return domain.Wrap(domain.CodeConflict, "immutable runtime record", err)
	case strings.Contains(v, "23514") || strings.Contains(v, "check constraint"):
		return domain.Wrap(domain.CodeInvalidArgument, "runtime database constraint violated", err)
	default:
		return err
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

func (a *txAdapter) GetApplication(id string) (domain.Application, bool) {
	v, err := scanPayload[domain.Application](a.tx.QueryRow(`SELECT payload FROM runtime.applications WHERE id=$1`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return v, false
	}
	a.capture(err)
	return v, err == nil
}
func (a *txAdapter) FindApplicationByTenantName(tenant, name string) (domain.Application, bool) {
	v, err := scanPayload[domain.Application](a.tx.QueryRow(`SELECT payload FROM runtime.applications WHERE tenant_id=$1 AND lower(name)=lower($2)`, tenant, name))
	if errors.Is(err, sql.ErrNoRows) {
		return v, false
	}
	a.capture(err)
	return v, err == nil
}
func (a *txAdapter) InsertApplication(v domain.Application) error {
	raw, err := marshal(v)
	if err != nil {
		return err
	}
	_, err = a.tx.Exec(`INSERT INTO runtime.applications(id,tenant_id,project_id,name,lifecycle,version,payload,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, v.ID, v.TenantID, v.ProjectID, v.Name, string(v.Lifecycle), v.Version, raw, v.CreatedAt, v.UpdatedAt)
	return mapDB(err)
}
func (a *txAdapter) UpdateApplication(v domain.Application, expected int64) error {
	raw, err := marshal(v)
	if err != nil {
		return err
	}
	result, err := a.tx.Exec(`UPDATE runtime.applications SET name=$1,lifecycle=$2,version=$3,payload=$4,updated_at=$5 WHERE id=$6 AND version=$7`, v.Name, string(v.Lifecycle), v.Version, raw, v.UpdatedAt, v.ID, expected)
	return affected(result, err, "application")
}

func (a *txAdapter) GetEnvironment(id string) (domain.Environment, bool) {
	v, err := scanPayload[domain.Environment](a.tx.QueryRow(`SELECT payload FROM runtime.environments WHERE id=$1`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return v, false
	}
	a.capture(err)
	return v, err == nil
}
func (a *txAdapter) FindEnvironmentByApplicationName(app, name string) (domain.Environment, bool) {
	v, err := scanPayload[domain.Environment](a.tx.QueryRow(`SELECT payload FROM runtime.environments WHERE application_id=$1 AND lower(name)=lower($2)`, app, name))
	if errors.Is(err, sql.ErrNoRows) {
		return v, false
	}
	a.capture(err)
	return v, err == nil
}
func (a *txAdapter) ListEnvironmentsByApplication(app string) []domain.Environment {
	rows, err := a.tx.Query(`SELECT payload FROM runtime.environments WHERE application_id=$1 ORDER BY created_at,id`, app)
	if err != nil {
		a.capture(err)
		return nil
	}
	defer rows.Close()
	out := []domain.Environment{}
	for rows.Next() {
		v, err := scanPayload[domain.Environment](rows)
		if err != nil {
			a.capture(err)
			return nil
		}
		out = append(out, v)
	}
	a.capture(rows.Err())
	return out
}
func (a *txAdapter) InsertEnvironment(v domain.Environment) error {
	raw, err := marshal(v)
	if err != nil {
		return err
	}
	_, err = a.tx.Exec(`INSERT INTO runtime.environments(id,tenant_id,application_id,name,namespace,is_default,active_release_id,placement_id,version,payload,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`, v.ID, v.TenantID, v.ApplicationID, v.Name, v.Namespace, v.Default, v.ActiveReleaseID, v.PlacementID, v.Version, raw, v.CreatedAt, v.UpdatedAt)
	return mapDB(err)
}
func (a *txAdapter) UpdateEnvironment(v domain.Environment, expected int64) error {
	raw, err := marshal(v)
	if err != nil {
		return err
	}
	result, err := a.tx.Exec(`UPDATE runtime.environments SET name=$1,is_default=$2,active_release_id=$3,placement_id=$4,version=$5,payload=$6,updated_at=$7 WHERE id=$8 AND version=$9`, v.Name, v.Default, v.ActiveReleaseID, v.PlacementID, v.Version, raw, v.UpdatedAt, v.ID, expected)
	return affected(result, err, "environment")
}

func (a *txAdapter) GetRuntimeCell(id string) (domain.RuntimeCell, bool) {
	v, err := scanPayload[domain.RuntimeCell](a.tx.QueryRow(`SELECT payload FROM runtime.runtime_cells WHERE id=$1`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return v, false
	}
	a.capture(err)
	return v, err == nil
}
func (a *txAdapter) ListRuntimeCells() []domain.RuntimeCell {
	rows, err := a.tx.Query(`SELECT payload FROM runtime.runtime_cells ORDER BY id`)
	if err != nil {
		a.capture(err)
		return nil
	}
	defer rows.Close()
	out := []domain.RuntimeCell{}
	for rows.Next() {
		v, err := scanPayload[domain.RuntimeCell](rows)
		if err != nil {
			a.capture(err)
			return nil
		}
		out = append(out, v)
	}
	a.capture(rows.Err())
	return out
}
func (a *txAdapter) InsertRuntimeCell(v domain.RuntimeCell) error {
	raw, err := marshal(v)
	if err != nil {
		return err
	}
	_, err = a.tx.Exec(`INSERT INTO runtime.runtime_cells(id,region,state,capacity_units,allocated_units,gitops_repository,version,payload,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, v.ID, v.Region, string(v.State), v.CapacityUnits, v.AllocatedUnits, v.GitOpsRepository, v.Version, raw, v.CreatedAt, v.UpdatedAt)
	return mapDB(err)
}
func (a *txAdapter) UpdateRuntimeCell(v domain.RuntimeCell, expected int64) error {
	raw, err := marshal(v)
	if err != nil {
		return err
	}
	result, err := a.tx.Exec(`UPDATE runtime.runtime_cells SET state=$1,capacity_units=$2,allocated_units=$3,version=$4,payload=$5,updated_at=$6 WHERE id=$7 AND version=$8`, string(v.State), v.CapacityUnits, v.AllocatedUnits, v.Version, raw, v.UpdatedAt, v.ID, expected)
	return affected(result, err, "runtime cell")
}

func (a *txAdapter) GetPlacement(id string) (domain.Placement, bool) {
	v, err := scanPayload[domain.Placement](a.tx.QueryRow(`SELECT payload FROM runtime.placements WHERE id=$1`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return v, false
	}
	a.capture(err)
	return v, err == nil
}
func (a *txAdapter) GetCurrentPlacement(env string) (domain.Placement, bool) {
	v, err := scanPayload[domain.Placement](a.tx.QueryRow(`SELECT payload FROM runtime.placements WHERE environment_id=$1 AND current`, env))
	if errors.Is(err, sql.ErrNoRows) {
		return v, false
	}
	a.capture(err)
	return v, err == nil
}
func (a *txAdapter) InsertPlacement(v domain.Placement) error {
	raw, err := marshal(v)
	if err != nil {
		return err
	}
	_, err = a.tx.Exec(`INSERT INTO runtime.placements(id,tenant_id,environment_id,cell_id,current,units,version,payload,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, v.ID, v.TenantID, v.EnvironmentID, v.CellID, v.Current, v.Units, v.Version, raw, v.CreatedAt, v.UpdatedAt)
	return mapDB(err)
}
func (a *txAdapter) UpdatePlacement(v domain.Placement, expected int64) error {
	raw, err := marshal(v)
	if err != nil {
		return err
	}
	result, err := a.tx.Exec(`UPDATE runtime.placements SET current=$1,units=$2,version=$3,payload=$4,updated_at=$5 WHERE id=$6 AND version=$7`, v.Current, v.Units, v.Version, raw, v.UpdatedAt, v.ID, expected)
	return affected(result, err, "placement")
}
func (a *txAdapter) InsertPlacementMigration(v domain.PlacementMigration) error {
	raw, err := marshal(v)
	if err != nil {
		return err
	}
	_, err = a.tx.Exec(`INSERT INTO runtime.placement_migrations(id,tenant_id,environment_id,from_placement_id,target_cell_id,state,payload,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, v.ID, v.TenantID, v.EnvironmentID, v.FromPlacementID, v.TargetCellID, v.State, raw, v.CreatedAt)
	return mapDB(err)
}

func (a *txAdapter) GetRelease(id string) (domain.Release, bool) {
	v, err := scanPayload[domain.Release](a.tx.QueryRow(`SELECT payload FROM runtime.releases WHERE id=$1`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return v, false
	}
	a.capture(err)
	return v, err == nil
}
func (a *txAdapter) FindReleaseByIdentity(tenant, env, identity string) (domain.Release, bool) {
	v, err := scanPayload[domain.Release](a.tx.QueryRow(`SELECT payload FROM runtime.releases WHERE tenant_id=$1 AND environment_id=$2 AND identity=$3 AND rollback_of=''`, tenant, env, identity))
	if errors.Is(err, sql.ErrNoRows) {
		return v, false
	}
	a.capture(err)
	return v, err == nil
}
func (a *txAdapter) ListReleasesByEnvironment(env string) []domain.Release {
	rows, err := a.tx.Query(`SELECT payload FROM runtime.releases WHERE environment_id=$1 ORDER BY created_at,id`, env)
	if err != nil {
		a.capture(err)
		return nil
	}
	defer rows.Close()
	out := []domain.Release{}
	for rows.Next() {
		v, err := scanPayload[domain.Release](rows)
		if err != nil {
			a.capture(err)
			return nil
		}
		out = append(out, v)
	}
	a.capture(rows.Err())
	return out
}
func (a *txAdapter) InsertRelease(v domain.Release) error {
	raw, err := marshal(v)
	if err != nil {
		return err
	}
	_, err = a.tx.Exec(`INSERT INTO runtime.releases(id,tenant_id,application_id,environment_id,identity,rollback_of,artifact_id,repository,digest,media_type,state,version,payload,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)`, v.ID, v.TenantID, v.ApplicationID, v.EnvironmentID, v.Identity, v.RollbackOf, v.Artifact.ArtifactID, v.Artifact.Repository, v.Artifact.Digest, v.Artifact.MediaType, string(v.State), v.Version, raw, v.CreatedAt, v.UpdatedAt)
	return mapDB(err)
}
func (a *txAdapter) UpdateRelease(v domain.Release, expected int64) error {
	raw, err := marshal(v)
	if err != nil {
		return err
	}
	result, err := a.tx.Exec(`UPDATE runtime.releases SET state=$1,version=$2,payload=$3,updated_at=$4 WHERE id=$5 AND version=$6`, string(v.State), v.Version, raw, v.UpdatedAt, v.ID, expected)
	return affected(result, err, "release")
}

func (a *txAdapter) GetDeployment(id string) (domain.Deployment, bool) {
	v, err := scanPayload[domain.Deployment](a.tx.QueryRow(`SELECT payload FROM runtime.deployments WHERE id=$1`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return v, false
	}
	a.capture(err)
	return v, err == nil
}
func (a *txAdapter) FindDeploymentByRelease(release string) (domain.Deployment, bool) {
	v, err := scanPayload[domain.Deployment](a.tx.QueryRow(`SELECT payload FROM runtime.deployments WHERE release_id=$1`, release))
	if errors.Is(err, sql.ErrNoRows) {
		return v, false
	}
	a.capture(err)
	return v, err == nil
}
func (a *txAdapter) ListDeploymentsByEnvironment(env string) []domain.Deployment {
	rows, err := a.tx.Query(`SELECT payload FROM runtime.deployments WHERE environment_id=$1 ORDER BY created_at,id`, env)
	if err != nil {
		a.capture(err)
		return nil
	}
	defer rows.Close()
	out := []domain.Deployment{}
	for rows.Next() {
		v, err := scanPayload[domain.Deployment](rows)
		if err != nil {
			a.capture(err)
			return nil
		}
		out = append(out, v)
	}
	a.capture(rows.Err())
	return out
}
func (a *txAdapter) InsertDeployment(v domain.Deployment) error {
	raw, err := marshal(v)
	if err != nil {
		return err
	}
	_, err = a.tx.Exec(`INSERT INTO runtime.deployments(id,tenant_id,application_id,environment_id,release_id,placement_id,phase,git_commit_sha,version,payload,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`, v.ID, v.TenantID, v.ApplicationID, v.EnvironmentID, v.ReleaseID, v.PlacementID, string(v.Phase), v.GitCommitSHA, v.Version, raw, v.CreatedAt, v.UpdatedAt)
	return mapDB(err)
}
func (a *txAdapter) UpdateDeployment(v domain.Deployment, expected int64) error {
	raw, err := marshal(v)
	if err != nil {
		return err
	}
	result, err := a.tx.Exec(`UPDATE runtime.deployments SET phase=$1,git_commit_sha=$2,version=$3,payload=$4,updated_at=$5 WHERE id=$6 AND version=$7`, string(v.Phase), v.GitCommitSHA, v.Version, raw, v.UpdatedAt, v.ID, expected)
	return affected(result, err, "deployment")
}

func (a *txAdapter) GetGitOpsCommitByRelease(release string) (domain.GitOpsCommitRecord, bool) {
	v, err := scanPayload[domain.GitOpsCommitRecord](a.tx.QueryRow(`SELECT payload FROM runtime.gitops_commits WHERE release_id=$1`, release))
	if errors.Is(err, sql.ErrNoRows) {
		return v, false
	}
	a.capture(err)
	return v, err == nil
}
func (a *txAdapter) InsertGitOpsCommit(v domain.GitOpsCommitRecord) error {
	raw, err := marshal(v)
	if err != nil {
		return err
	}
	_, err = a.tx.Exec(`INSERT INTO runtime.gitops_commits(id,tenant_id,cell_id,release_id,deployment_id,path,manifest_hash,commit_sha,payload,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, v.ID, v.TenantID, v.CellID, v.ReleaseID, v.DeploymentID, v.Path, v.ManifestHash, v.CommitSHA, raw, v.CreatedAt)
	return mapDB(err)
}
func (a *txAdapter) InsertQuarantine(v domain.QuarantineRecord) error {
	raw, err := marshal(v)
	if err != nil {
		return err
	}
	_, err = a.tx.Exec(`INSERT INTO runtime.quarantine(id,cell_id,namespace,kind,name,reason,payload,observed_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, v.ID, v.CellID, v.Namespace, v.Kind, v.Name, v.Reason, raw, v.ObservedAt)
	return mapDB(err)
}

func (a *txAdapter) GetIdempotency(tenant, scope, key string) (domain.IdempotencyRecord, bool) {
	var v domain.IdempotencyRecord
	err := a.tx.QueryRow(`SELECT tenant_id,scope,key,request_hash,resource_id,created_at FROM runtime.idempotency WHERE tenant_id=$1 AND scope=$2 AND key=$3`, tenant, scope, key).Scan(&v.TenantID, &v.Scope, &v.Key, &v.RequestHash, &v.ResourceID, &v.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return v, false
	}
	a.capture(err)
	return v, err == nil
}
func (a *txAdapter) InsertIdempotency(v domain.IdempotencyRecord) error {
	_, err := a.tx.Exec(`INSERT INTO runtime.idempotency(tenant_id,scope,key,request_hash,resource_id,created_at) VALUES($1,$2,$3,$4,$5,$6)`, v.TenantID, v.Scope, v.Key, v.RequestHash, v.ResourceID, v.CreatedAt)
	return mapDB(err)
}
func (a *txAdapter) AppendOutbox(v domain.OutboxRecord) error {
	_, err := a.tx.Exec(`INSERT INTO runtime.outbox(id,tenant_id,topic,aggregate_id,payload,created_at) VALUES($1,$2,$3,$4,$5,$6)`, v.ID, v.TenantID, v.Topic, v.AggregateID, v.Payload, v.CreatedAt)
	return mapDB(err)
}
func (a *txAdapter) AppendAudit(v domain.AuditRecord) error {
	_, err := a.tx.Exec(`INSERT INTO runtime.audit(id,tenant_id,actor_id,action,resource_type,resource_id,data,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, v.ID, v.TenantID, v.ActorID, v.Action, v.ResourceType, v.ResourceID, v.Data, v.CreatedAt)
	return mapDB(err)
}

var _ application.Store = (*Store)(nil)
