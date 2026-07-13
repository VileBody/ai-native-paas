package memory

import (
	"context"
	"sync"

	"github.com/keir-research/ai-native-paas/internal/source/application"
	"github.com/keir-research/ai-native-paas/internal/source/domain"
)

type state struct {
	projects      map[string]domain.Project
	repositories  map[string]domain.Repository
	branches      map[string]domain.BranchHead
	mergeRequests map[string]domain.MergeRequest
	workspaces    map[string]domain.Workspace
	idempotency   map[string]application.IdempotencyRecord
	webhooks      map[string]application.WebhookReceipt
	outbox        []application.OutboxRecord
	audit         []application.AuditRecord
}
type Store struct {
	mu sync.Mutex
	s  state
}

func New() *Store { return &Store{s: newState()} }
func newState() state {
	return state{projects: map[string]domain.Project{}, repositories: map[string]domain.Repository{}, branches: map[string]domain.BranchHead{}, mergeRequests: map[string]domain.MergeRequest{}, workspaces: map[string]domain.Workspace{}, idempotency: map[string]application.IdempotencyRecord{}, webhooks: map[string]application.WebhookReceipt{}}
}
func clone(in state) state {
	out := newState()
	for k, v := range in.projects {
		out.projects[k] = v
	}
	for k, v := range in.repositories {
		out.repositories[k] = v
	}
	for k, v := range in.branches {
		out.branches[k] = v
	}
	for k, v := range in.mergeRequests {
		out.mergeRequests[k] = v
	}
	for k, v := range in.workspaces {
		out.workspaces[k] = v
	}
	for k, v := range in.idempotency {
		v.Result = append([]byte(nil), v.Result...)
		out.idempotency[k] = v
	}
	for k, v := range in.webhooks {
		out.webhooks[k] = v
	}
	out.outbox = append([]application.OutboxRecord(nil), in.outbox...)
	out.audit = append([]application.AuditRecord(nil), in.audit...)
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

func (t *tx) GetProject(id string) (domain.Project, bool) { v, ok := t.projects[id]; return v, ok }
func (t *tx) FindProjectBySlug(tenant, slug string) (domain.Project, bool) {
	for _, v := range t.projects {
		if v.TenantID == tenant && v.Slug == slug {
			return v, true
		}
	}
	return domain.Project{}, false
}
func (t *tx) InsertProject(v domain.Project) error {
	if _, ok := t.projects[v.ID]; ok {
		return domain.NewError(domain.CodeConflict, "project exists")
	}
	if _, ok := t.FindProjectBySlug(v.TenantID, v.Slug); ok {
		return domain.NewError(domain.CodeConflict, "project slug exists")
	}
	t.projects[v.ID] = v
	return nil
}
func (t *tx) UpdateProject(v domain.Project, expected int64) error {
	old, ok := t.projects[v.ID]
	if !ok {
		return domain.NewError(domain.CodeNotFound, "project not found")
	}
	if old.Version != expected {
		return domain.NewError(domain.CodeStaleVersion, "project version mismatch")
	}
	for _, p := range t.projects {
		if p.ID != v.ID && p.TenantID == v.TenantID && p.Slug == v.Slug {
			return domain.NewError(domain.CodeConflict, "project slug exists")
		}
	}
	t.projects[v.ID] = v
	return nil
}
func (t *tx) GetRepository(id string) (domain.Repository, bool) {
	v, ok := t.repositories[id]
	return v, ok
}
func (t *tx) FindRepositoryByProject(id string) (domain.Repository, bool) {
	for _, v := range t.repositories {
		if v.ProjectID == id {
			return v, true
		}
	}
	return domain.Repository{}, false
}
func (t *tx) FindRepositoryByProviderID(provider string, id int64) (domain.Repository, bool) {
	for _, v := range t.repositories {
		if v.Provider == provider && v.ProviderProjectID == id {
			return v, true
		}
	}
	return domain.Repository{}, false
}
func (t *tx) InsertRepository(v domain.Repository) error {
	if _, ok := t.repositories[v.ID]; ok {
		return domain.NewError(domain.CodeConflict, "repository exists")
	}
	if _, ok := t.FindRepositoryByProject(v.ProjectID); ok {
		return domain.NewError(domain.CodeConflict, "project repository exists")
	}
	t.repositories[v.ID] = v
	return nil
}
func (t *tx) UpdateRepository(v domain.Repository, expected int64) error {
	old, ok := t.repositories[v.ID]
	if !ok {
		return domain.NewError(domain.CodeNotFound, "repository not found")
	}
	if old.Version != expected {
		return domain.NewError(domain.CodeStaleVersion, "repository version mismatch")
	}
	if old.ProviderProjectID != 0 && v.ProviderProjectID != old.ProviderProjectID {
		return domain.NewError(domain.CodeConflict, "provider identity immutable")
	}
	t.repositories[v.ID] = v
	return nil
}
func (t *tx) ListRepositories() []domain.Repository {
	out := make([]domain.Repository, 0, len(t.repositories))
	for _, v := range t.repositories {
		out = append(out, v)
	}
	return out
}
func branchKey(repo, name string) string { return repo + "\x00" + name }
func (t *tx) GetBranch(repo, name string) (domain.BranchHead, bool) {
	v, ok := t.branches[branchKey(repo, name)]
	return v, ok
}
func (t *tx) UpsertBranch(v domain.BranchHead, expected int64) error {
	k := branchKey(v.RepositoryID, v.Name)
	old, ok := t.branches[k]
	if ok && old.Version != expected {
		return domain.NewError(domain.CodeStaleVersion, "branch version mismatch")
	}
	if !ok && expected != 0 {
		return domain.NewError(domain.CodeStaleVersion, "branch does not exist")
	}
	t.branches[k] = v
	return nil
}
func mrKey(repo string, iid int64) string { return repo + "\x00" + fmtInt(iid) }
func (t *tx) GetMergeRequest(repo string, iid int64) (domain.MergeRequest, bool) {
	v, ok := t.mergeRequests[mrKey(repo, iid)]
	return v, ok
}
func (t *tx) UpsertMergeRequest(v domain.MergeRequest, expected int64) error {
	k := mrKey(v.RepositoryID, v.ProviderIID)
	old, ok := t.mergeRequests[k]
	if ok && old.Version != expected {
		return domain.NewError(domain.CodeStaleVersion, "merge request version mismatch")
	}
	if !ok && expected != 0 {
		return domain.NewError(domain.CodeStaleVersion, "merge request missing")
	}
	t.mergeRequests[k] = v
	return nil
}
func (t *tx) GetWorkspace(id string) (domain.Workspace, bool) {
	v, ok := t.workspaces[id]
	return v, ok
}
func (t *tx) InsertWorkspace(v domain.Workspace) error {
	if _, ok := t.workspaces[v.ID]; ok {
		return domain.NewError(domain.CodeConflict, "workspace exists")
	}
	t.workspaces[v.ID] = v
	return nil
}
func (t *tx) UpdateWorkspace(v domain.Workspace, expected int64) error {
	old, ok := t.workspaces[v.ID]
	if !ok {
		return domain.NewError(domain.CodeNotFound, "workspace not found")
	}
	if old.Version != expected {
		return domain.NewError(domain.CodeStaleVersion, "workspace version mismatch")
	}
	t.workspaces[v.ID] = v
	return nil
}
func idemKey(tenant, key string) string { return tenant + "\x00" + key }
func (t *tx) GetIdempotency(tenant, key string) (application.IdempotencyRecord, bool) {
	v, ok := t.idempotency[idemKey(tenant, key)]
	return v, ok
}
func (t *tx) PutIdempotency(v application.IdempotencyRecord) error {
	k := idemKey(v.TenantID, v.Key)
	if old, ok := t.idempotency[k]; ok && (old.RequestHash != v.RequestHash || old.Command != v.Command) {
		return domain.NewError(domain.CodeConflict, "idempotency conflict")
	}
	t.idempotency[k] = v
	return nil
}
func (t *tx) ReceiveWebhook(v application.WebhookReceipt) (bool, error) {
	k := v.Provider + "\x00" + v.EventID
	if old, ok := t.webhooks[k]; ok {
		if old.BodyHash != v.BodyHash {
			return false, domain.NewError(domain.CodeConflict, "webhook id reused with another body")
		}
		return false, nil
	}
	t.webhooks[k] = v
	return true, nil
}
func (t *tx) AppendOutbox(v application.OutboxRecord) error {
	t.outbox = append(t.outbox, v)
	return nil
}
func (t *tx) AppendAudit(v application.AuditRecord) error {
	v.Data = append([]byte(nil), v.Data...)
	t.audit = append(t.audit, v)
	return nil
}
func fmtInt(v int64) string {
	if v == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	neg := v < 0
	if neg {
		v = -v
	}
	for v > 0 {
		i--
		b[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
func (s *Store) Snapshot() (projects []domain.Project, repos []domain.Repository, outbox []application.OutboxRecord, audit []application.AuditRecord) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, v := range s.s.projects {
		projects = append(projects, v)
	}
	for _, v := range s.s.repositories {
		repos = append(repos, v)
	}
	outbox = append(outbox, s.s.outbox...)
	audit = append(audit, s.s.audit...)
	return
}
