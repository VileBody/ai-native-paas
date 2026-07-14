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

	"github.com/keir-research/ai-native-paas/internal/build/application"
	"github.com/keir-research/ai-native-paas/internal/build/domain"
	buildv1 "github.com/keir-research/ai-native-paas/pkg/contracts/build/v1"
	buildv2 "github.com/keir-research/ai-native-paas/pkg/contracts/build/v2"
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
	return &Store{DB: db, MaxSerializableRetries: 6}, nil
}

func (s *Store) Migrate(ctx context.Context) error {
	if s == nil || s.DB == nil {
		return errors.New("postgres db is nil")
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
		if _, err = s.DB.ExecContext(ctx, `CREATE SCHEMA IF NOT EXISTS build`); err != nil {
			return err
		}
		if _, err = s.DB.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS build.schema_migrations (version text PRIMARY KEY, checksum text NOT NULL, applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
			return err
		}
		var existing string
		scanErr := s.DB.QueryRowContext(ctx, `SELECT checksum FROM build.schema_migrations WHERE version=$1`, entry.Name()).Scan(&existing)
		if scanErr == nil {
			if existing != checksum {
				return fmt.Errorf("build migration checksum mismatch: %s", entry.Name())
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
			_, err = tx.ExecContext(ctx, `INSERT INTO build.schema_migrations(version,checksum) VALUES($1,$2)`, entry.Name(), checksum)
		}
		if err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("apply build migration %s: %w", entry.Name(), err)
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
		attempts = 6
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
			if !isSerialization(err) && !isUniqueViolation(err) {
				return err
			}
			last = err
		} else if err = tx.Commit(); err == nil {
			return nil
		} else if !isSerialization(err) {
			return err
		} else {
			last = err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(attempt+1) * 5 * time.Millisecond):
		}
	}
	if domain.HasCode(last, domain.CodeConflict) {
		return last
	}
	return domain.Wrap(domain.CodeUnavailable, "serializable transaction retry budget exhausted", last)
}

func isUniqueViolation(err error) bool {
	value := strings.ToLower(fmt.Sprint(err))
	return strings.Contains(value, "23505") || strings.Contains(value, "duplicate key") || strings.Contains(value, "unique constraint")
}

func isSerialization(err error) bool {
	value := strings.ToLower(fmt.Sprint(err))
	return strings.Contains(value, "40001") || strings.Contains(value, "40p01") || strings.Contains(value, "serialization") || strings.Contains(value, "deadlock detected")
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

const buildColumns = `id,tenant_id,identity,attempt,source_revision,config,builder_digest,run_image_digest,platform_build_version,runtime,backend,buildpack_id,state,correlation_id,original_correlation_id,artifact_id,failure_code,failure_message,retryable,version,created_at,updated_at,started_at,completed_at`

func scanBuild(row interface{ Scan(...any) error }) (domain.Build, error) {
	var value domain.Build
	var sourceRaw, configRaw []byte
	var state, backend string
	var artifactID sql.NullString
	var startedAt, completedAt sql.NullTime
	err := row.Scan(&value.ID, &value.TenantID, &value.Identity, &value.Attempt, &sourceRaw, &configRaw, &value.BuilderDigest, &value.RunImageDigest, &value.PlatformBuildVersion, &value.Runtime, &backend, &value.BuildpackID, &state, &value.CorrelationID, &value.OriginalCorrelationID, &artifactID, &value.FailureCode, &value.FailureMessage, &value.Retryable, &value.Version, &value.CreatedAt, &value.UpdatedAt, &startedAt, &completedAt)
	if err != nil {
		return value, err
	}
	if err := json.Unmarshal(sourceRaw, &value.Source); err != nil {
		return value, fmt.Errorf("decode source revision: %w", err)
	}
	var stored persistedBuildConfig
	if err := json.Unmarshal(configRaw, &stored); err != nil {
		return value, fmt.Errorf("decode build config: %w", err)
	}
	value.Config = stored.BuildConfig
	value.RequestContract = stored.RequestContract
	value.BuildSpec = stored.BuildSpec
	value.BuildSpecDigest = stored.BuildSpecDigest
	if stored.AutoDetectionAllowed != nil {
		value.AutoDetectionAllowed = *stored.AutoDetectionAllowed
	}
	if value.RequestContract == "" {
		value.RequestContract = domain.LegacyBuildContract
		value.AutoDetectionAllowed = true
	}
	value.State = buildv1.BuildState(state)
	value.Backend = domain.ExecutionBackend(backend)
	if artifactID.Valid {
		value.ArtifactID = artifactID.String
	}
	if startedAt.Valid {
		value.StartedAt = startedAt.Time
	}
	if completedAt.Valid {
		value.CompletedAt = completedAt.Time
	}
	return value, nil
}

// persistedBuildConfig keeps additive request metadata in the existing
// immutable config JSONB. Anonymous embedding preserves the original flat v1
// representation, so databases created before v2 remain readable.
type persistedBuildConfig struct {
	domain.BuildConfig
	RequestContract      string             `json:"_request_contract,omitempty"`
	BuildSpec            *buildv2.BuildSpec `json:"_build_spec,omitempty"`
	BuildSpecDigest      string             `json:"_build_spec_digest,omitempty"`
	AutoDetectionAllowed *bool              `json:"_auto_detection_allowed,omitempty"`
}

func (a *txAdapter) GetBuild(id string) (domain.Build, bool) {
	value, err := scanBuild(a.tx.QueryRow(`SELECT `+buildColumns+` FROM build.builds WHERE id=$1`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return value, false
	}
	a.capture(err)
	return value, err == nil
}

func (a *txAdapter) FindBuildByIdentity(tenantID, identity string) (domain.Build, bool) {
	value, err := scanBuild(a.tx.QueryRow(`SELECT `+buildColumns+` FROM build.builds WHERE tenant_id=$1 AND identity=$2 ORDER BY attempt DESC, created_at DESC, id DESC LIMIT 1`, tenantID, identity))
	if errors.Is(err, sql.ErrNoRows) {
		return value, false
	}
	a.capture(err)
	return value, err == nil
}

func encodeBuild(value domain.Build) ([]byte, []byte, error) {
	sourceRaw, err := json.Marshal(value.Source)
	if err != nil {
		return nil, nil, err
	}
	// Preserve the byte-level JSONB shape of pre-v2 rows. The database treats
	// config as immutable, so enriching a legacy row during a state transition
	// would correctly be rejected as an identity mutation.
	if value.RequestContract == domain.LegacyBuildContract && value.BuildSpec == nil && value.BuildSpecDigest == "" && value.AutoDetectionAllowed {
		configRaw, err := json.Marshal(value.Config)
		return sourceRaw, configRaw, err
	}
	autoDetectionAllowed := value.AutoDetectionAllowed
	configRaw, err := json.Marshal(persistedBuildConfig{
		BuildConfig: value.Config, RequestContract: value.RequestContract,
		BuildSpec: value.BuildSpec, BuildSpecDigest: value.BuildSpecDigest,
		AutoDetectionAllowed: &autoDetectionAllowed,
	})
	return sourceRaw, configRaw, err
}

func (a *txAdapter) InsertBuild(value domain.Build) error {
	sourceRaw, configRaw, err := encodeBuild(value)
	if err != nil {
		return domain.Wrap(domain.CodePlatformFailure, "encode build", err)
	}
	_, err = a.tx.Exec(`INSERT INTO build.builds(`+buildColumns+`) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24)`,
		value.ID, value.TenantID, value.Identity, value.Attempt, sourceRaw, configRaw, value.BuilderDigest, value.RunImageDigest, value.PlatformBuildVersion, value.Runtime, string(value.Backend), value.BuildpackID, string(value.State), value.CorrelationID, value.OriginalCorrelationID, nullString(value.ArtifactID), value.FailureCode, value.FailureMessage, value.Retryable, value.Version, value.CreatedAt, value.UpdatedAt, nullTime(value.StartedAt), nullTime(value.CompletedAt))
	return mapDB(err)
}

func (a *txAdapter) UpdateBuild(value domain.Build, expected int64) error {
	sourceRaw, configRaw, err := encodeBuild(value)
	if err != nil {
		return domain.Wrap(domain.CodePlatformFailure, "encode build", err)
	}
	result, err := a.tx.Exec(`UPDATE build.builds SET source_revision=$1,config=$2,builder_digest=$3,run_image_digest=$4,platform_build_version=$5,runtime=$6,backend=$7,buildpack_id=$8,state=$9,correlation_id=$10,original_correlation_id=$11,artifact_id=$12,failure_code=$13,failure_message=$14,retryable=$15,version=$16,updated_at=$17,started_at=$18,completed_at=$19 WHERE id=$20 AND version=$21`,
		sourceRaw, configRaw, value.BuilderDigest, value.RunImageDigest, value.PlatformBuildVersion, value.Runtime, string(value.Backend), value.BuildpackID, string(value.State), value.CorrelationID, value.OriginalCorrelationID, nullString(value.ArtifactID), value.FailureCode, value.FailureMessage, value.Retryable, value.Version, value.UpdatedAt, nullTime(value.StartedAt), nullTime(value.CompletedAt), value.ID, expected)
	return affected(result, err, "build")
}

func (a *txAdapter) ListBuildsByProject(tenantID, projectID string) []domain.Build {
	rows, err := a.tx.Query(`SELECT `+buildColumns+` FROM build.builds WHERE tenant_id=$1 AND source_revision->>'project_id'=$2 ORDER BY created_at,id`, tenantID, projectID)
	if err != nil {
		a.capture(err)
		return nil
	}
	defer rows.Close()
	var out []domain.Build
	for rows.Next() {
		value, err := scanBuild(rows)
		if err != nil {
			a.capture(err)
			return nil
		}
		out = append(out, value)
	}
	a.capture(rows.Err())
	return out
}

const artifactColumns = `id,tenant_id,build_id,repository,digest,media_type,state,sbom_digest,sbom_media_type,rejection_code,rejection_notes,version,created_at,updated_at`

func scanArtifactBase(row interface{ Scan(...any) error }) (domain.Artifact, error) {
	var value domain.Artifact
	var state string
	var notes []byte
	err := row.Scan(&value.ID, &value.TenantID, &value.BuildID, &value.Repository, &value.Digest, &value.MediaType, &state, &value.SBOMDigest, &value.SBOMMediaType, &value.RejectionCode, &notes, &value.Version, &value.CreatedAt, &value.UpdatedAt)
	if err != nil {
		return value, err
	}
	value.State = domain.ArtifactState(state)
	if len(notes) > 0 {
		if err := json.Unmarshal(notes, &value.RejectionNotes); err != nil {
			return value, err
		}
	}
	return value, nil
}

func (a *txAdapter) loadArtifact(query string, args ...any) (domain.Artifact, bool) {
	value, err := scanArtifactBase(a.tx.QueryRow(query, args...))
	if errors.Is(err, sql.ErrNoRows) {
		return value, false
	}
	if err != nil {
		a.capture(err)
		return value, false
	}
	var scan domain.ScanResult
	var reasons []byte
	err = a.tx.QueryRow(`SELECT scanner,policy_version,passed,highest_severity,findings_digest,reasons,scanned_at FROM build.scan_results WHERE artifact_id=$1`, value.ID).Scan(&scan.Scanner, &scan.PolicyVersion, &scan.Passed, &scan.HighestSeverity, &scan.FindingsDigest, &reasons, &scan.ScannedAt)
	if err == nil {
		if unmarshalErr := json.Unmarshal(reasons, &scan.Reasons); unmarshalErr != nil {
			a.capture(unmarshalErr)
			return value, false
		}
		value.Scan = &scan
	} else if !errors.Is(err, sql.ErrNoRows) {
		a.capture(err)
		return value, false
	}
	var signature domain.SignatureRecord
	err = a.tx.QueryRow(`SELECT issuer,algorithm,digest,signature,attachment_digest,signed_at FROM build.signature_records WHERE artifact_id=$1`, value.ID).Scan(&signature.Issuer, &signature.Algorithm, &signature.Digest, &signature.Signature, &signature.AttachmentDigest, &signature.SignedAt)
	if err == nil {
		value.Signature = &signature
	} else if !errors.Is(err, sql.ErrNoRows) {
		a.capture(err)
		return value, false
	}
	return value, true
}

func (a *txAdapter) GetArtifact(id string) (domain.Artifact, bool) {
	return a.loadArtifact(`SELECT `+artifactColumns+` FROM build.artifacts WHERE id=$1`, id)
}
func (a *txAdapter) FindArtifactByBuild(buildID string) (domain.Artifact, bool) {
	return a.loadArtifact(`SELECT `+artifactColumns+` FROM build.artifacts WHERE build_id=$1`, buildID)
}

func (a *txAdapter) InsertArtifact(value domain.Artifact) error {
	notes, err := json.Marshal(value.RejectionNotes)
	if err != nil {
		return err
	}
	_, err = a.tx.Exec(`INSERT INTO build.artifacts(`+artifactColumns+`) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)`, value.ID, value.TenantID, value.BuildID, value.Repository, value.Digest, value.MediaType, string(value.State), value.SBOMDigest, value.SBOMMediaType, value.RejectionCode, notes, value.Version, value.CreatedAt, value.UpdatedAt)
	if err != nil {
		return mapDB(err)
	}
	return a.upsertArtifactChildren(value)
}

func (a *txAdapter) UpdateArtifact(value domain.Artifact, expected int64) error {
	notes, err := json.Marshal(value.RejectionNotes)
	if err != nil {
		return err
	}
	// Trust children are written first so the database can enforce that a
	// RELEASABLE parent already has a passing scan and digest-bound signature.
	// Any stale parent update rolls the entire serializable transaction back.
	if err := a.upsertArtifactChildren(value); err != nil {
		return err
	}
	result, err := a.tx.Exec(`UPDATE build.artifacts SET tenant_id=$1,build_id=$2,repository=$3,digest=$4,media_type=$5,state=$6,sbom_digest=$7,sbom_media_type=$8,rejection_code=$9,rejection_notes=$10,version=$11,updated_at=$12 WHERE id=$13 AND version=$14`, value.TenantID, value.BuildID, value.Repository, value.Digest, value.MediaType, string(value.State), value.SBOMDigest, value.SBOMMediaType, value.RejectionCode, notes, value.Version, value.UpdatedAt, value.ID, expected)
	return affected(result, err, "artifact")
}

func (a *txAdapter) upsertArtifactChildren(value domain.Artifact) error {
	if value.Scan != nil {
		reasons, err := json.Marshal(value.Scan.Reasons)
		if err != nil {
			return err
		}
		_, err = a.tx.Exec(`INSERT INTO build.scan_results(artifact_id,scanner,policy_version,passed,highest_severity,findings_digest,reasons,scanned_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT(artifact_id) DO UPDATE SET scanner=EXCLUDED.scanner,policy_version=EXCLUDED.policy_version,passed=EXCLUDED.passed,highest_severity=EXCLUDED.highest_severity,findings_digest=EXCLUDED.findings_digest,reasons=EXCLUDED.reasons,scanned_at=EXCLUDED.scanned_at`, value.ID, value.Scan.Scanner, value.Scan.PolicyVersion, value.Scan.Passed, value.Scan.HighestSeverity, value.Scan.FindingsDigest, reasons, value.Scan.ScannedAt)
		if err != nil {
			return mapDB(err)
		}
	}
	if value.Signature != nil {
		_, err := a.tx.Exec(`INSERT INTO build.signature_records(artifact_id,issuer,algorithm,digest,signature,attachment_digest,signed_at) VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT(artifact_id) DO UPDATE SET issuer=EXCLUDED.issuer,algorithm=EXCLUDED.algorithm,digest=EXCLUDED.digest,signature=EXCLUDED.signature,attachment_digest=EXCLUDED.attachment_digest,signed_at=EXCLUDED.signed_at`, value.ID, value.Signature.Issuer, value.Signature.Algorithm, value.Signature.Digest, value.Signature.Signature, value.Signature.AttachmentDigest, value.Signature.SignedAt)
		if err != nil {
			return mapDB(err)
		}
	}
	return nil
}

func (a *txAdapter) GetIdempotency(tenantID, key string) (application.IdempotencyRecord, bool) {
	var value application.IdempotencyRecord
	var completedAt sql.NullTime
	err := a.tx.QueryRow(`SELECT tenant_id,key,command,request_hash,result,completed,created_at,completed_at FROM build.idempotency WHERE tenant_id=$1 AND key=$2`, tenantID, key).Scan(&value.TenantID, &value.Key, &value.Command, &value.RequestHash, &value.Result, &value.Completed, &value.CreatedAt, &completedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return value, false
	}
	if completedAt.Valid {
		value.CompletedAt = completedAt.Time
	}
	a.capture(err)
	return value, err == nil
}

func (a *txAdapter) PutIdempotency(value application.IdempotencyRecord) error {
	result, err := a.tx.Exec(`INSERT INTO build.idempotency(tenant_id,key,command,request_hash,result,completed,created_at,completed_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT(tenant_id,key) DO NOTHING`, value.TenantID, value.Key, value.Command, value.RequestHash, value.Result, value.Completed, value.CreatedAt, nullTime(value.CompletedAt))
	if err != nil {
		return mapDB(err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 1 {
		return nil
	}
	var command, requestHash string
	if err := a.tx.QueryRow(`SELECT command,request_hash FROM build.idempotency WHERE tenant_id=$1 AND key=$2`, value.TenantID, value.Key).Scan(&command, &requestHash); err != nil {
		return err
	}
	if command != value.Command || requestHash != value.RequestHash {
		return domain.NewError(domain.CodeConflict, "idempotency key was used for another request")
	}
	_, err = a.tx.Exec(`UPDATE build.idempotency SET result=$1,completed=$2,completed_at=$3 WHERE tenant_id=$4 AND key=$5`, value.Result, value.Completed, nullTime(value.CompletedAt), value.TenantID, value.Key)
	return mapDB(err)
}

func (a *txAdapter) AppendOutbox(value application.OutboxRecord) error {
	_, err := a.tx.Exec(`INSERT INTO build.outbox(id,topic,aggregate_id,payload,created_at) VALUES($1,$2,$3,$4,$5)`, value.ID, value.Topic, value.AggregateID, value.Payload, value.CreatedAt)
	return mapDB(err)
}
func (a *txAdapter) AppendAudit(value application.AuditRecord) error {
	_, err := a.tx.Exec(`INSERT INTO build.audit(id,tenant_id,actor_id,action,resource_type,resource_id,data,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, value.ID, value.TenantID, value.ActorID, value.Action, value.ResourceType, value.ResourceID, value.Data, value.CreatedAt)
	return mapDB(err)
}

func affected(result sql.Result, err error, what string) error {
	if err != nil {
		return mapDB(err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return domain.NewError(domain.CodeStaleVersion, what+" version mismatch")
	}
	return nil
}
func nullTime(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return value
}
func nullString(value string) any {
	if value == "" {
		return nil
	}
	return value
}
func mapDB(err error) error {
	if err == nil {
		return nil
	}
	value := strings.ToLower(err.Error())
	if strings.Contains(value, "duplicate key") || strings.Contains(value, "unique constraint") || strings.Contains(value, "23505") {
		return domain.Wrap(domain.CodeConflict, "database uniqueness conflict", err)
	}
	if strings.Contains(value, "55000") || strings.Contains(value, "23514") || strings.Contains(value, "check constraint") {
		return domain.Wrap(domain.CodeConflict, "database invariant violation", err)
	}
	return err
}

var _ application.Store = (*Store)(nil)
