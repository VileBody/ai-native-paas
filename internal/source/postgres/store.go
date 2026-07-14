package postgres

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
	"time"

	"github.com/keir-research/ai-native-paas/internal/source/application"
	"github.com/keir-research/ai-native-paas/internal/source/domain"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

type Store struct {
	DB                     *sql.DB
	MaxSerializableRetries int
}

func (s *Store) Migrate(ctx context.Context) error {
	if s.DB == nil {
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
		if _, err = s.DB.ExecContext(ctx, "CREATE SCHEMA IF NOT EXISTS source"); err != nil {
			return err
		}
		if _, err = s.DB.ExecContext(ctx, "CREATE TABLE IF NOT EXISTS source.schema_migrations (version text PRIMARY KEY, checksum text NOT NULL, applied_at timestamptz NOT NULL DEFAULT now())"); err != nil {
			return err
		}
		var existing string
		scanErr := s.DB.QueryRowContext(ctx, "SELECT checksum FROM source.schema_migrations WHERE version=$1", entry.Name()).Scan(&existing)
		if scanErr == nil {
			if existing != checksum {
				return fmt.Errorf("migration checksum mismatch: %s", entry.Name())
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
			_, err = tx.ExecContext(ctx, "INSERT INTO source.schema_migrations(version,checksum) VALUES($1,$2)", entry.Name(), checksum)
		}
		if err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("apply migration %s: %w", entry.Name(), err)
		}
		if err = tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}
func (s *Store) Transact(ctx context.Context, fn func(application.Tx) error) error {
	if s.DB == nil {
		return domain.NewError(domain.CodeUnavailable, "postgres db is nil")
	}
	attempts := s.MaxSerializableRetries
	if attempts <= 0 {
		attempts = 4
	}
	var last error
	for i := 0; i < attempts; i++ {
		tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
		if err != nil {
			return err
		}
		a := &txAdapter{tx: tx}
		err = fn(a)
		if err == nil && a.err != nil {
			err = a.err
		}
		if err != nil {
			_ = tx.Rollback()
			return err
		}
		if err = tx.Commit(); err == nil {
			return nil
		}
		last = err
		if !isSerialization(err) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(i+1) * 5 * time.Millisecond):
		}
	}
	return domain.Wrap(domain.CodeUnavailable, "serializable transaction retry budget exhausted", last)
}
func isSerialization(err error) bool {
	v := strings.ToLower(fmt.Sprint(err))
	return strings.Contains(v, "40001") || strings.Contains(v, "serialization") || strings.Contains(v, "deadlock detected")
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
func (a *txAdapter) GetProject(id string) (domain.Project, bool) {
	var p domain.Project
	err := a.tx.QueryRow("SELECT id,tenant_id,name,slug,version,created_at,updated_at FROM source.projects WHERE id=$1", id).Scan(&p.ID, &p.TenantID, &p.Name, &p.Slug, &p.Version, &p.CreatedAt, &p.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return p, false
	}
	a.capture(err)
	return p, err == nil
}
func (a *txAdapter) FindProjectBySlug(tenant, slug string) (domain.Project, bool) {
	var p domain.Project
	err := a.tx.QueryRow("SELECT id,tenant_id,name,slug,version,created_at,updated_at FROM source.projects WHERE tenant_id=$1 AND slug=$2", tenant, slug).Scan(&p.ID, &p.TenantID, &p.Name, &p.Slug, &p.Version, &p.CreatedAt, &p.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return p, false
	}
	a.capture(err)
	return p, err == nil
}
func (a *txAdapter) InsertProject(p domain.Project) error {
	_, err := a.tx.Exec("INSERT INTO source.projects(id,tenant_id,name,slug,version,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7)", p.ID, p.TenantID, p.Name, p.Slug, p.Version, p.CreatedAt, p.UpdatedAt)
	return mapDB(err)
}
func (a *txAdapter) UpdateProject(p domain.Project, expected int64) error {
	res, err := a.tx.Exec("UPDATE source.projects SET name=$1,slug=$2,version=$3,updated_at=$4 WHERE id=$5 AND version=$6", p.Name, p.Slug, p.Version, p.UpdatedAt, p.ID, expected)
	return affected(res, err, "project")
}
func scanRepo(row interface{ Scan(...any) error }) (domain.Repository, error) {
	var r domain.Repository
	var providerID sql.NullInt64
	err := row.Scan(&r.ID, &r.TenantID, &r.ProjectID, &r.Provider, &r.ProviderNamespaceID, &providerID, &r.ProviderPath, &r.WebURL, &r.DefaultBranch, &r.BootstrapRevision, &r.State, &r.LastError, &r.CorrelationID, &r.Version, &r.CreatedAt, &r.UpdatedAt)
	if providerID.Valid {
		r.ProviderProjectID = providerID.Int64
	}
	return r, err
}

const repoColumns = "id,tenant_id,project_id,provider,provider_namespace_id,provider_project_id,provider_path,web_url,default_branch,bootstrap_revision,state,last_error,correlation_id,version,created_at,updated_at"

func (a *txAdapter) GetRepository(id string) (domain.Repository, bool) {
	r, err := scanRepo(a.tx.QueryRow("SELECT "+repoColumns+" FROM source.repositories WHERE id=$1", id))
	if errors.Is(err, sql.ErrNoRows) {
		return r, false
	}
	a.capture(err)
	return r, err == nil
}
func (a *txAdapter) FindRepositoryByProject(id string) (domain.Repository, bool) {
	r, err := scanRepo(a.tx.QueryRow("SELECT "+repoColumns+" FROM source.repositories WHERE project_id=$1", id))
	if errors.Is(err, sql.ErrNoRows) {
		return r, false
	}
	a.capture(err)
	return r, err == nil
}
func (a *txAdapter) FindRepositoryByProviderID(provider string, id int64) (domain.Repository, bool) {
	r, err := scanRepo(a.tx.QueryRow("SELECT "+repoColumns+" FROM source.repositories WHERE provider=$1 AND provider_project_id=$2", provider, id))
	if errors.Is(err, sql.ErrNoRows) {
		return r, false
	}
	a.capture(err)
	return r, err == nil
}
func (a *txAdapter) InsertRepository(r domain.Repository) error {
	var pid any
	if r.ProviderProjectID > 0 {
		pid = r.ProviderProjectID
	}
	_, err := a.tx.Exec("INSERT INTO source.repositories("+repoColumns+") VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)", r.ID, r.TenantID, r.ProjectID, r.Provider, r.ProviderNamespaceID, pid, r.ProviderPath, r.WebURL, r.DefaultBranch, r.BootstrapRevision, r.State, r.LastError, r.CorrelationID, r.Version, r.CreatedAt, r.UpdatedAt)
	return mapDB(err)
}
func (a *txAdapter) UpdateRepository(r domain.Repository, expected int64) error {
	var pid any
	if r.ProviderProjectID > 0 {
		pid = r.ProviderProjectID
	}
	res, err := a.tx.Exec("UPDATE source.repositories SET provider_project_id=$1,provider_path=$2,web_url=$3,default_branch=$4,bootstrap_revision=$5,state=$6,last_error=$7,version=$8,updated_at=$9 WHERE id=$10 AND version=$11", pid, r.ProviderPath, r.WebURL, r.DefaultBranch, r.BootstrapRevision, r.State, r.LastError, r.Version, r.UpdatedAt, r.ID, expected)
	return affected(res, err, "repository")
}
func (a *txAdapter) ListRepositories() []domain.Repository {
	rows, err := a.tx.Query("SELECT " + repoColumns + " FROM source.repositories ORDER BY id")
	if err != nil {
		a.capture(err)
		return nil
	}
	defer rows.Close()
	var out []domain.Repository
	for rows.Next() {
		r, scanErr := scanRepo(rows)
		if scanErr != nil {
			a.capture(scanErr)
			return nil
		}
		out = append(out, r)
	}
	a.capture(rows.Err())
	return out
}
func scanProviderQuarantine(row interface{ Scan(...any) error }) (domain.ProviderQuarantine, error) {
	var value domain.ProviderQuarantine
	err := row.Scan(
		&value.Provider, &value.ProviderNamespaceID, &value.ProviderProjectID, &value.ProviderPath, &value.WebURL,
		&value.CandidateRepositoryID, &value.Reason, &value.ExternalIdentityMatched, &value.ManagedLabelPresent,
		&value.FirstObservedAt, &value.LastObservedAt, &value.Version,
	)
	return value, err
}

const providerQuarantineColumns = "provider,provider_namespace_id,provider_project_id,provider_path,web_url,candidate_repository_id,reason,external_identity_matched,managed_label_present,first_observed_at,last_observed_at,version"

func (a *txAdapter) GetProviderQuarantine(provider string, providerProjectID int64) (domain.ProviderQuarantine, bool) {
	value, err := scanProviderQuarantine(a.tx.QueryRow("SELECT "+providerQuarantineColumns+" FROM source.provider_project_quarantine WHERE provider=$1 AND provider_project_id=$2", provider, providerProjectID))
	if errors.Is(err, sql.ErrNoRows) {
		return value, false
	}
	a.capture(err)
	return value, err == nil
}
func (a *txAdapter) UpsertProviderQuarantine(value domain.ProviderQuarantine, expected int64) error {
	if expected == 0 {
		_, err := a.tx.Exec("INSERT INTO source.provider_project_quarantine("+providerQuarantineColumns+") VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)", value.Provider, value.ProviderNamespaceID, value.ProviderProjectID, value.ProviderPath, value.WebURL, value.CandidateRepositoryID, value.Reason, value.ExternalIdentityMatched, value.ManagedLabelPresent, value.FirstObservedAt, value.LastObservedAt, value.Version)
		return mapDB(err)
	}
	res, err := a.tx.Exec("UPDATE source.provider_project_quarantine SET provider_namespace_id=$1,provider_path=$2,web_url=$3,candidate_repository_id=$4,reason=$5,external_identity_matched=$6,managed_label_present=$7,last_observed_at=$8,version=$9 WHERE provider=$10 AND provider_project_id=$11 AND version=$12", value.ProviderNamespaceID, value.ProviderPath, value.WebURL, value.CandidateRepositoryID, value.Reason, value.ExternalIdentityMatched, value.ManagedLabelPresent, value.LastObservedAt, value.Version, value.Provider, value.ProviderProjectID, expected)
	return affected(res, err, "provider quarantine")
}
func (a *txAdapter) GetBranch(repo, name string) (domain.BranchHead, bool) {
	var b domain.BranchHead
	var eventAt, observedAt, deletedAt sql.NullTime
	err := a.tx.QueryRow("SELECT repository_id,name,commit_sha,environment_id,last_event_id,last_event_at,observed_at,deleted_at,version FROM source.branch_heads WHERE repository_id=$1 AND name=$2", repo, name).Scan(&b.RepositoryID, &b.Name, &b.CommitSHA, &b.EnvironmentID, &b.LastEventID, &eventAt, &observedAt, &deletedAt, &b.Version)
	if eventAt.Valid {
		b.LastEventAt = eventAt.Time
	}
	if observedAt.Valid {
		b.ObservedAt = observedAt.Time
	}
	if deletedAt.Valid {
		b.DeletedAt = deletedAt.Time
	}
	if errors.Is(err, sql.ErrNoRows) {
		return b, false
	}
	a.capture(err)
	return b, err == nil
}
func (a *txAdapter) UpsertBranch(b domain.BranchHead, expected int64) error {
	if expected == 0 {
		_, err := a.tx.Exec("INSERT INTO source.branch_heads(repository_id,name,commit_sha,environment_id,last_event_id,last_event_at,observed_at,deleted_at,version) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)", b.RepositoryID, b.Name, b.CommitSHA, b.EnvironmentID, b.LastEventID, nullTime(b.LastEventAt), nullTime(b.ObservedAt), nullTime(b.DeletedAt), b.Version)
		return mapDB(err)
	}
	res, err := a.tx.Exec("UPDATE source.branch_heads SET commit_sha=$1,environment_id=$2,last_event_id=$3,last_event_at=$4,observed_at=$5,deleted_at=$6,version=$7 WHERE repository_id=$8 AND name=$9 AND version=$10", b.CommitSHA, b.EnvironmentID, b.LastEventID, nullTime(b.LastEventAt), nullTime(b.ObservedAt), nullTime(b.DeletedAt), b.Version, b.RepositoryID, b.Name, expected)
	return affected(res, err, "branch")
}
func (a *txAdapter) GetMergeRequest(repo string, iid int64) (domain.MergeRequest, bool) {
	var m domain.MergeRequest
	err := a.tx.QueryRow("SELECT repository_id,provider_iid,source_branch,target_branch,head_sha,state,version,updated_at FROM source.merge_requests WHERE repository_id=$1 AND provider_iid=$2", repo, iid).Scan(&m.RepositoryID, &m.ProviderIID, &m.SourceBranch, &m.TargetBranch, &m.HeadSHA, &m.State, &m.Version, &m.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return m, false
	}
	a.capture(err)
	return m, err == nil
}
func (a *txAdapter) UpsertMergeRequest(m domain.MergeRequest, expected int64) error {
	if expected == 0 {
		_, err := a.tx.Exec("INSERT INTO source.merge_requests(repository_id,provider_iid,source_branch,target_branch,head_sha,state,version,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8)", m.RepositoryID, m.ProviderIID, m.SourceBranch, m.TargetBranch, m.HeadSHA, m.State, m.Version, m.UpdatedAt)
		return mapDB(err)
	}
	res, err := a.tx.Exec("UPDATE source.merge_requests SET source_branch=$1,target_branch=$2,head_sha=$3,state=$4,version=$5,updated_at=$6 WHERE repository_id=$7 AND provider_iid=$8 AND version=$9", m.SourceBranch, m.TargetBranch, m.HeadSHA, m.State, m.Version, m.UpdatedAt, m.RepositoryID, m.ProviderIID, expected)
	return affected(res, err, "merge request")
}
func (a *txAdapter) GetWorkspace(id string) (domain.Workspace, bool) {
	var w domain.Workspace
	err := a.tx.QueryRow("SELECT id,tenant_id,repository_id,branch,base_sha,commit_sha,directory,credential_id,state,last_error,expires_at,version,created_at,updated_at FROM source.workspaces WHERE id=$1", id).Scan(&w.ID, &w.TenantID, &w.RepositoryID, &w.Branch, &w.BaseSHA, &w.CommitSHA, &w.Directory, &w.CredentialID, &w.State, &w.LastError, &w.ExpiresAt, &w.Version, &w.CreatedAt, &w.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return w, false
	}
	a.capture(err)
	return w, err == nil
}
func (a *txAdapter) ListWorkspaces(repositoryID string) []domain.Workspace {
	rows, err := a.tx.Query("SELECT id,tenant_id,repository_id,branch,base_sha,commit_sha,directory,credential_id,state,last_error,expires_at,version,created_at,updated_at FROM source.workspaces WHERE repository_id=$1 ORDER BY id", repositoryID)
	if err != nil {
		a.capture(err)
		return nil
	}
	defer rows.Close()
	var result []domain.Workspace
	for rows.Next() {
		var workspace domain.Workspace
		if err := rows.Scan(&workspace.ID, &workspace.TenantID, &workspace.RepositoryID, &workspace.Branch, &workspace.BaseSHA, &workspace.CommitSHA, &workspace.Directory, &workspace.CredentialID, &workspace.State, &workspace.LastError, &workspace.ExpiresAt, &workspace.Version, &workspace.CreatedAt, &workspace.UpdatedAt); err != nil {
			a.capture(err)
			return nil
		}
		result = append(result, workspace)
	}
	a.capture(rows.Err())
	return result
}
func (a *txAdapter) InsertWorkspace(w domain.Workspace) error {
	_, err := a.tx.Exec("INSERT INTO source.workspaces(id,tenant_id,repository_id,branch,base_sha,commit_sha,directory,credential_id,state,last_error,expires_at,version,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)", w.ID, w.TenantID, w.RepositoryID, w.Branch, w.BaseSHA, w.CommitSHA, w.Directory, w.CredentialID, w.State, w.LastError, w.ExpiresAt, w.Version, w.CreatedAt, w.UpdatedAt)
	return mapDB(err)
}
func (a *txAdapter) UpdateWorkspace(w domain.Workspace, expected int64) error {
	res, err := a.tx.Exec("UPDATE source.workspaces SET branch=$1,base_sha=$2,commit_sha=$3,directory=$4,credential_id=$5,state=$6,last_error=$7,expires_at=$8,version=$9,updated_at=$10 WHERE id=$11 AND version=$12", w.Branch, w.BaseSHA, w.CommitSHA, w.Directory, w.CredentialID, w.State, w.LastError, w.ExpiresAt, w.Version, w.UpdatedAt, w.ID, expected)
	return affected(res, err, "workspace")
}
func (a *txAdapter) GetIdempotency(tenant, key string) (application.IdempotencyRecord, bool) {
	var r application.IdempotencyRecord
	var completedAt sql.NullTime
	err := a.tx.QueryRow("SELECT tenant_id,key,command,request_hash,result,completed,created_at,completed_at FROM source.idempotency WHERE tenant_id=$1 AND key=$2", tenant, key).Scan(&r.TenantID, &r.Key, &r.Command, &r.RequestHash, &r.Result, &r.Completed, &r.CreatedAt, &completedAt)
	if completedAt.Valid {
		r.CompletedAt = completedAt.Time
	}
	if errors.Is(err, sql.ErrNoRows) {
		return r, false
	}
	a.capture(err)
	return r, err == nil
}
func (a *txAdapter) PutIdempotency(r application.IdempotencyRecord) error {
	_, err := a.tx.Exec("INSERT INTO source.idempotency(tenant_id,key,command,request_hash,result,completed,created_at,completed_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT(tenant_id,key) DO UPDATE SET result=EXCLUDED.result,completed=EXCLUDED.completed,completed_at=EXCLUDED.completed_at WHERE source.idempotency.command=EXCLUDED.command AND source.idempotency.request_hash=EXCLUDED.request_hash", r.TenantID, r.Key, r.Command, r.RequestHash, r.Result, r.Completed, r.CreatedAt, nullTime(r.CompletedAt))
	return mapDB(err)
}
func (a *txAdapter) ReceiveWebhook(r application.WebhookReceipt) (bool, error) {
	res, err := a.tx.Exec("INSERT INTO source.webhook_receipts(provider,event_id,body_hash,received_at) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING", r.Provider, r.EventID, r.BodyHash, r.ReceivedAt)
	if err != nil {
		return false, mapDB(err)
	}
	n, _ := res.RowsAffected()
	if n == 1 {
		return true, nil
	}
	var hash string
	if err = a.tx.QueryRow("SELECT body_hash FROM source.webhook_receipts WHERE provider=$1 AND event_id=$2", r.Provider, r.EventID).Scan(&hash); err != nil {
		return false, err
	}
	if hash != r.BodyHash {
		return false, domain.NewError(domain.CodeConflict, "webhook id reused with another body")
	}
	return false, nil
}
func (a *txAdapter) AppendOutbox(r application.OutboxRecord) error {
	_, err := a.tx.Exec("INSERT INTO source.outbox(id,topic,aggregate_id,payload,created_at) VALUES($1,$2,$3,$4,$5)", r.ID, r.Topic, r.AggregateID, r.Payload, r.CreatedAt)
	return mapDB(err)
}
func (a *txAdapter) AppendAudit(r application.AuditRecord) error {
	_, err := a.tx.Exec("INSERT INTO source.audit(id,tenant_id,actor_id,action,resource_type,resource_id,data,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8)", r.ID, r.TenantID, r.ActorID, r.Action, r.ResourceType, r.ResourceID, r.Data, r.CreatedAt)
	return mapDB(err)
}
func nullTime(v time.Time) any {
	if v.IsZero() {
		return nil
	}
	return v
}
func affected(res sql.Result, err error, what string) error {
	if err != nil {
		return mapDB(err)
	}
	n, e := res.RowsAffected()
	if e != nil {
		return e
	}
	if n != 1 {
		return domain.NewError(domain.CodeStaleVersion, what+" version mismatch")
	}
	return nil
}
func mapDB(err error) error {
	if err == nil {
		return nil
	}
	v := strings.ToLower(err.Error())
	if strings.Contains(v, "duplicate key") || strings.Contains(v, "unique constraint") || strings.Contains(v, "23505") {
		return domain.Wrap(domain.CodeConflict, "database uniqueness conflict", err)
	}
	return err
}
