package memory

import (
	"context"
	"encoding/json"
	"reflect"
	"sort"
	"sync"

	"github.com/keir-research/ai-native-paas/internal/runtime/application"
	"github.com/keir-research/ai-native-paas/internal/runtime/domain"
	runtimev1 "github.com/keir-research/ai-native-paas/pkg/contracts/runtime/v1"
)

type state struct {
	applications map[string]domain.Application
	environments map[string]domain.Environment
	cells        map[string]domain.RuntimeCell
	placements   map[string]domain.Placement
	migrations   map[string]domain.PlacementMigration
	releases     map[string]domain.Release
	deployments  map[string]domain.Deployment
	commits      map[string]domain.GitOpsCommitRecord
	quarantine   map[string]domain.QuarantineRecord
	idempotency  map[string]domain.IdempotencyRecord
	outbox       []domain.OutboxRecord
	audit        []domain.AuditRecord
}

type Store struct {
	mu sync.Mutex
	s  state
}

func New() *Store { return &Store{s: newState()} }
func newState() state {
	return state{
		applications: map[string]domain.Application{}, environments: map[string]domain.Environment{}, cells: map[string]domain.RuntimeCell{}, placements: map[string]domain.Placement{}, migrations: map[string]domain.PlacementMigration{}, releases: map[string]domain.Release{}, deployments: map[string]domain.Deployment{}, commits: map[string]domain.GitOpsCommitRecord{}, quarantine: map[string]domain.QuarantineRecord{}, idempotency: map[string]domain.IdempotencyRecord{},
	}
}
func cloneValue[T any](in T) T {
	raw, _ := json.Marshal(in)
	var out T
	_ = json.Unmarshal(raw, &out)
	return out
}
func clone(in state) state {
	out := newState()
	for k, v := range in.applications {
		out.applications[k] = cloneValue(v)
	}
	for k, v := range in.environments {
		out.environments[k] = cloneValue(v)
	}
	for k, v := range in.cells {
		out.cells[k] = cloneValue(v)
	}
	for k, v := range in.placements {
		out.placements[k] = cloneValue(v)
	}
	for k, v := range in.migrations {
		out.migrations[k] = cloneValue(v)
	}
	for k, v := range in.releases {
		out.releases[k] = cloneValue(v)
	}
	for k, v := range in.deployments {
		out.deployments[k] = cloneValue(v)
	}
	for k, v := range in.commits {
		out.commits[k] = cloneValue(v)
	}
	for k, v := range in.quarantine {
		out.quarantine[k] = cloneValue(v)
	}
	for k, v := range in.idempotency {
		out.idempotency[k] = cloneValue(v)
	}
	out.outbox = cloneValue(in.outbox)
	out.audit = cloneValue(in.audit)
	return out
}
func (s *Store) Transact(ctx context.Context, fn func(application.Tx) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	next := clone(s.s)
	if err := fn((*tx)(&next)); err != nil {
		return err
	}
	s.s = next
	return nil
}

type tx state

func (t *tx) GetApplication(id string) (domain.Application, bool) {
	v, ok := t.applications[id]
	return cloneValue(v), ok
}
func (t *tx) FindApplicationByTenantName(tenant, name string) (domain.Application, bool) {
	for _, v := range t.applications {
		if v.TenantID == tenant && v.Name == name {
			return cloneValue(v), true
		}
	}
	return domain.Application{}, false
}
func (t *tx) InsertApplication(v domain.Application) error {
	if _, ok := t.applications[v.ID]; ok {
		return domain.NewError(domain.CodeConflict, "application exists")
	}
	if _, ok := t.FindApplicationByTenantName(v.TenantID, v.Name); ok {
		return domain.NewError(domain.CodeConflict, "application name exists")
	}
	t.applications[v.ID] = cloneValue(v)
	return nil
}
func (t *tx) UpdateApplication(v domain.Application, expected int64) error {
	old, ok := t.applications[v.ID]
	if !ok {
		return domain.NewError(domain.CodeNotFound, "application not found")
	}
	if old.Version != expected {
		return domain.NewError(domain.CodeStaleVersion, "application version mismatch")
	}
	if old.TenantID != v.TenantID || old.ProjectID != v.ProjectID || !old.CreatedAt.Equal(v.CreatedAt) {
		return domain.NewError(domain.CodeConflict, "application identity is immutable")
	}
	for _, other := range t.applications {
		if other.ID != v.ID && other.TenantID == v.TenantID && other.Name == v.Name {
			return domain.NewError(domain.CodeConflict, "application name exists")
		}
	}
	t.applications[v.ID] = cloneValue(v)
	return nil
}

func (t *tx) GetEnvironment(id string) (domain.Environment, bool) {
	v, ok := t.environments[id]
	return cloneValue(v), ok
}
func (t *tx) FindEnvironmentByApplicationName(app, name string) (domain.Environment, bool) {
	for _, v := range t.environments {
		if v.ApplicationID == app && v.Name == name {
			return cloneValue(v), true
		}
	}
	return domain.Environment{}, false
}
func (t *tx) ListEnvironmentsByApplication(app string) []domain.Environment {
	out := []domain.Environment{}
	for _, v := range t.environments {
		if v.ApplicationID == app {
			out = append(out, cloneValue(v))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
func (t *tx) InsertEnvironment(v domain.Environment) error {
	if _, ok := t.environments[v.ID]; ok {
		return domain.NewError(domain.CodeConflict, "environment exists")
	}
	for _, other := range t.environments {
		if other.ApplicationID == v.ApplicationID && other.Name == v.Name {
			return domain.NewError(domain.CodeConflict, "environment name exists")
		}
		if v.Default && other.ApplicationID == v.ApplicationID && other.Default {
			return domain.NewError(domain.CodeConflict, "default environment exists")
		}
		if other.Namespace == v.Namespace {
			return domain.NewError(domain.CodeConflict, "environment namespace exists")
		}
	}
	t.environments[v.ID] = cloneValue(v)
	return nil
}
func (t *tx) UpdateEnvironment(v domain.Environment, expected int64) error {
	old, ok := t.environments[v.ID]
	if !ok {
		return domain.NewError(domain.CodeNotFound, "environment not found")
	}
	if old.Version != expected {
		return domain.NewError(domain.CodeStaleVersion, "environment version mismatch")
	}
	if old.TenantID != v.TenantID || old.ApplicationID != v.ApplicationID || old.Namespace != v.Namespace || !old.CreatedAt.Equal(v.CreatedAt) {
		return domain.NewError(domain.CodeConflict, "environment identity is immutable")
	}
	for _, other := range t.environments {
		if other.ID == v.ID {
			continue
		}
		if other.ApplicationID == v.ApplicationID && other.Name == v.Name {
			return domain.NewError(domain.CodeConflict, "environment name exists")
		}
		if v.Default && other.ApplicationID == v.ApplicationID && other.Default {
			return domain.NewError(domain.CodeConflict, "default environment exists")
		}
	}
	t.environments[v.ID] = cloneValue(v)
	return nil
}

func (t *tx) GetRuntimeCell(id string) (domain.RuntimeCell, bool) {
	v, ok := t.cells[id]
	return cloneValue(v), ok
}
func (t *tx) ListRuntimeCells() []domain.RuntimeCell {
	out := make([]domain.RuntimeCell, 0, len(t.cells))
	for _, v := range t.cells {
		out = append(out, cloneValue(v))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
func (t *tx) InsertRuntimeCell(v domain.RuntimeCell) error {
	if _, ok := t.cells[v.ID]; ok {
		return domain.NewError(domain.CodeConflict, "runtime cell exists")
	}
	t.cells[v.ID] = cloneValue(v)
	return nil
}
func (t *tx) UpdateRuntimeCell(v domain.RuntimeCell, expected int64) error {
	old, ok := t.cells[v.ID]
	if !ok {
		return domain.NewError(domain.CodeNotFound, "runtime cell not found")
	}
	if old.Version != expected {
		return domain.NewError(domain.CodeStaleVersion, "runtime cell version mismatch")
	}
	configured := old
	configured.State = v.State
	configured.AllocatedUnits = v.AllocatedUnits
	configured.CapacityUnits = v.CapacityUnits
	configured.Version = v.Version
	configured.UpdatedAt = v.UpdatedAt
	if !reflect.DeepEqual(configured, v) {
		return domain.NewError(domain.CodeConflict, "runtime cell identity is immutable")
	}
	t.cells[v.ID] = cloneValue(v)
	return nil
}

func (t *tx) GetPlacement(id string) (domain.Placement, bool) {
	v, ok := t.placements[id]
	return cloneValue(v), ok
}
func (t *tx) GetCurrentPlacement(env string) (domain.Placement, bool) {
	for _, v := range t.placements {
		if v.EnvironmentID == env && v.Current {
			return cloneValue(v), true
		}
	}
	return domain.Placement{}, false
}
func (t *tx) InsertPlacement(v domain.Placement) error {
	if _, ok := t.placements[v.ID]; ok {
		return domain.NewError(domain.CodeConflict, "placement exists")
	}
	if v.Current {
		if _, ok := t.GetCurrentPlacement(v.EnvironmentID); ok {
			return domain.NewError(domain.CodeConflict, "current placement exists")
		}
	}
	t.placements[v.ID] = cloneValue(v)
	return nil
}
func (t *tx) UpdatePlacement(v domain.Placement, expected int64) error {
	old, ok := t.placements[v.ID]
	if !ok {
		return domain.NewError(domain.CodeNotFound, "placement not found")
	}
	if old.Version != expected {
		return domain.NewError(domain.CodeStaleVersion, "placement version mismatch")
	}
	if old.TenantID != v.TenantID || old.EnvironmentID != v.EnvironmentID || old.CellID != v.CellID || old.Region != v.Region || old.Isolation != v.Isolation || !old.CreatedAt.Equal(v.CreatedAt) {
		return domain.NewError(domain.CodeConflict, "placement identity is immutable")
	}
	if v.Current {
		for _, other := range t.placements {
			if other.ID != v.ID && other.EnvironmentID == v.EnvironmentID && other.Current {
				return domain.NewError(domain.CodeConflict, "current placement exists")
			}
		}
	}
	t.placements[v.ID] = cloneValue(v)
	return nil
}
func (t *tx) InsertPlacementMigration(v domain.PlacementMigration) error {
	if _, ok := t.migrations[v.ID]; ok {
		return domain.NewError(domain.CodeConflict, "placement migration exists")
	}
	t.migrations[v.ID] = cloneValue(v)
	return nil
}

func (t *tx) GetRelease(id string) (domain.Release, bool) {
	v, ok := t.releases[id]
	return cloneValue(v), ok
}
func (t *tx) FindReleaseByIdentity(tenant, env, identity string) (domain.Release, bool) {
	for _, v := range t.releases {
		if v.TenantID == tenant && v.EnvironmentID == env && v.Identity == identity && v.RollbackOf == "" {
			return cloneValue(v), true
		}
	}
	return domain.Release{}, false
}
func (t *tx) ListReleasesByEnvironment(env string) []domain.Release {
	out := []domain.Release{}
	for _, v := range t.releases {
		if v.EnvironmentID == env {
			out = append(out, cloneValue(v))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out
}
func (t *tx) InsertRelease(v domain.Release) error {
	if _, ok := t.releases[v.ID]; ok {
		return domain.NewError(domain.CodeConflict, "release exists")
	}
	if v.RollbackOf == "" {
		if _, ok := t.FindReleaseByIdentity(v.TenantID, v.EnvironmentID, v.Identity); ok {
			return domain.NewError(domain.CodeConflict, "release identity exists")
		}
	}
	if v.State == runtimev1.ReleaseActive {
		for _, other := range t.releases {
			if other.EnvironmentID == v.EnvironmentID && other.State == runtimev1.ReleaseActive {
				return domain.NewError(domain.CodeConflict, "active release exists")
			}
		}
	}
	t.releases[v.ID] = cloneValue(v)
	return nil
}
func (t *tx) UpdateRelease(v domain.Release, expected int64) error {
	old, ok := t.releases[v.ID]
	if !ok {
		return domain.NewError(domain.CodeNotFound, "release not found")
	}
	if old.Version != expected {
		return domain.NewError(domain.CodeStaleVersion, "release version mismatch")
	}
	identityOld, identityNew := old, v
	identityOld.State = identityNew.State
	identityOld.Version = identityNew.Version
	identityOld.UpdatedAt = identityNew.UpdatedAt
	identityOld.FailureCode = identityNew.FailureCode
	identityOld.FailureMessage = identityNew.FailureMessage
	if !reflect.DeepEqual(identityOld, identityNew) {
		return domain.NewError(domain.CodeConflict, "release identity is immutable")
	}
	if v.State == runtimev1.ReleaseActive {
		for _, other := range t.releases {
			if other.ID != v.ID && other.EnvironmentID == v.EnvironmentID && other.State == runtimev1.ReleaseActive {
				return domain.NewError(domain.CodeConflict, "active release exists")
			}
		}
	}
	t.releases[v.ID] = cloneValue(v)
	return nil
}

func (t *tx) GetDeployment(id string) (domain.Deployment, bool) {
	v, ok := t.deployments[id]
	return cloneValue(v), ok
}
func (t *tx) FindDeploymentByRelease(release string) (domain.Deployment, bool) {
	for _, v := range t.deployments {
		if v.ReleaseID == release {
			return cloneValue(v), true
		}
	}
	return domain.Deployment{}, false
}
func (t *tx) ListDeploymentsByEnvironment(env string) []domain.Deployment {
	out := []domain.Deployment{}
	for _, v := range t.deployments {
		if v.EnvironmentID == env {
			out = append(out, cloneValue(v))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out
}
func (t *tx) InsertDeployment(v domain.Deployment) error {
	if _, ok := t.deployments[v.ID]; ok {
		return domain.NewError(domain.CodeConflict, "deployment exists")
	}
	if _, ok := t.FindDeploymentByRelease(v.ReleaseID); ok {
		return domain.NewError(domain.CodeConflict, "release deployment exists")
	}
	t.deployments[v.ID] = cloneValue(v)
	return nil
}
func (t *tx) UpdateDeployment(v domain.Deployment, expected int64) error {
	old, ok := t.deployments[v.ID]
	if !ok {
		return domain.NewError(domain.CodeNotFound, "deployment not found")
	}
	if old.Version != expected {
		return domain.NewError(domain.CodeStaleVersion, "deployment version mismatch")
	}
	identityOld, identityNew := old, v
	identityOld.Phase = identityNew.Phase
	identityOld.GitCommitSHA = identityNew.GitCommitSHA
	identityOld.URL = identityNew.URL
	identityOld.ReadyReplicas = identityNew.ReadyReplicas
	identityOld.FailureCode = identityNew.FailureCode
	identityOld.FailureMessage = identityNew.FailureMessage
	identityOld.Version = identityNew.Version
	identityOld.UpdatedAt = identityNew.UpdatedAt
	if !reflect.DeepEqual(identityOld, identityNew) {
		return domain.NewError(domain.CodeConflict, "deployment identity is immutable")
	}
	t.deployments[v.ID] = cloneValue(v)
	return nil
}

func (t *tx) GetGitOpsCommitByRelease(release string) (domain.GitOpsCommitRecord, bool) {
	v, ok := t.commits[release]
	return cloneValue(v), ok
}
func (t *tx) InsertGitOpsCommit(v domain.GitOpsCommitRecord) error {
	if old, ok := t.commits[v.ReleaseID]; ok {
		if reflect.DeepEqual(old, v) {
			return nil
		}
		return domain.NewError(domain.CodeConflict, "GitOps commit exists")
	}
	t.commits[v.ReleaseID] = cloneValue(v)
	return nil
}
func (t *tx) InsertQuarantine(v domain.QuarantineRecord) error {
	if _, ok := t.quarantine[v.ID]; ok {
		return domain.NewError(domain.CodeConflict, "quarantine record exists")
	}
	t.quarantine[v.ID] = cloneValue(v)
	return nil
}

func idemKey(tenant, scope, key string) string { return tenant + "\x00" + scope + "\x00" + key }
func (t *tx) GetIdempotency(tenant, scope, key string) (domain.IdempotencyRecord, bool) {
	v, ok := t.idempotency[idemKey(tenant, scope, key)]
	return cloneValue(v), ok
}
func (t *tx) InsertIdempotency(v domain.IdempotencyRecord) error {
	k := idemKey(v.TenantID, v.Scope, v.Key)
	if old, ok := t.idempotency[k]; ok {
		if old.RequestHash == v.RequestHash && old.ResourceID == v.ResourceID {
			return nil
		}
		return domain.NewError(domain.CodeConflict, "idempotency record exists")
	}
	t.idempotency[k] = cloneValue(v)
	return nil
}
func (t *tx) AppendOutbox(v domain.OutboxRecord) error {
	for _, old := range t.outbox {
		if old.ID == v.ID {
			return domain.NewError(domain.CodeConflict, "outbox record exists")
		}
	}
	t.outbox = append(t.outbox, cloneValue(v))
	return nil
}
func (t *tx) AppendAudit(v domain.AuditRecord) error {
	for _, old := range t.audit {
		if old.ID == v.ID {
			return domain.NewError(domain.CodeConflict, "audit record exists")
		}
	}
	t.audit = append(t.audit, cloneValue(v))
	return nil
}

func (s *Store) Snapshot() Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	v := clone(s.s)
	return Snapshot{Applications: v.applications, Environments: v.environments, Cells: v.cells, Placements: v.placements, Migrations: v.migrations, Releases: v.releases, Deployments: v.deployments, Commits: v.commits, Quarantine: v.quarantine, Outbox: v.outbox, Audit: v.audit}
}

type Snapshot struct {
	Applications map[string]domain.Application
	Environments map[string]domain.Environment
	Cells        map[string]domain.RuntimeCell
	Placements   map[string]domain.Placement
	Migrations   map[string]domain.PlacementMigration
	Releases     map[string]domain.Release
	Deployments  map[string]domain.Deployment
	Commits      map[string]domain.GitOpsCommitRecord
	Quarantine   map[string]domain.QuarantineRecord
	Outbox       []domain.OutboxRecord
	Audit        []domain.AuditRecord
}

var _ application.Store = (*Store)(nil)
