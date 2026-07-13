package memory

import (
	"context"
	"github.com/keir-research/ai-native-paas/internal/agent/application"
	"github.com/keir-research/ai-native-paas/internal/agent/domain"
	"sort"
	"sync"
)

type data struct {
	principals  map[string]domain.AgentPrincipal
	tasks       map[string]domain.AgentTask
	invocations map[string]domain.Invocation
	approvals   map[string]domain.ApprovalRequest
	grants      map[string]domain.ApprovalGrant
	audit       []domain.AuditRecord
	outbox      []domain.OutboxRecord
}

func newData() data {
	return data{principals: map[string]domain.AgentPrincipal{}, tasks: map[string]domain.AgentTask{}, invocations: map[string]domain.Invocation{}, approvals: map[string]domain.ApprovalRequest{}, grants: map[string]domain.ApprovalGrant{}}
}
func (d data) clone() data {
	n := newData()
	for k, v := range d.principals {
		v.Scopes = append([]string(nil), v.Scopes...)
		n.principals[k] = v
	}
	for k, v := range d.tasks {
		n.tasks[k] = v
	}
	for k, v := range d.invocations {
		v.Response = append([]byte(nil), v.Response...)
		n.invocations[k] = v
	}
	for k, v := range d.approvals {
		n.approvals[k] = v
	}
	for k, v := range d.grants {
		if v.ConsumedAt != nil {
			x := *v.ConsumedAt
			v.ConsumedAt = &x
		}
		n.grants[k] = v
	}
	n.audit = append([]domain.AuditRecord(nil), d.audit...)
	n.outbox = make([]domain.OutboxRecord, len(d.outbox))
	for i, v := range d.outbox {
		v.Payload = append([]byte(nil), v.Payload...)
		n.outbox[i] = v
	}
	return n
}

type Store struct {
	mu sync.Mutex
	d  data
}

func New() *Store { return &Store{d: newData()} }
func (s *Store) Transact(ctx context.Context, fn func(application.Tx) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.d.clone()
	tx := &transaction{d: &c}
	if err := fn(tx); err != nil {
		return err
	}
	s.d = c
	return nil
}

type transaction struct{ d *data }

func (t *transaction) GetPrincipal(id string) (domain.AgentPrincipal, bool) {
	v, ok := t.d.principals[id]
	v.Scopes = append([]string(nil), v.Scopes...)
	return v, ok
}
func (t *transaction) InsertPrincipal(v domain.AgentPrincipal) error {
	if _, ok := t.d.principals[v.ID]; ok {
		return domain.NewError(domain.CodeConflict, "principal exists")
	}
	t.d.principals[v.ID] = v
	return nil
}
func (t *transaction) UpdatePrincipal(v domain.AgentPrincipal, e int64) error {
	o, ok := t.d.principals[v.ID]
	if !ok {
		return domain.NewError(domain.CodeNotFound, "principal not found")
	}
	if o.Version != e {
		return domain.NewError(domain.CodeConflict, "principal stale")
	}
	t.d.principals[v.ID] = v
	return nil
}
func (t *transaction) GetTask(id string) (domain.AgentTask, bool) {
	v, ok := t.d.tasks[id]
	return v, ok
}
func (t *transaction) InsertTask(v domain.AgentTask) error {
	if _, ok := t.d.tasks[v.ID]; ok {
		return domain.NewError(domain.CodeConflict, "task exists")
	}
	t.d.tasks[v.ID] = v
	return nil
}
func (t *transaction) UpdateTask(v domain.AgentTask, e int64) error {
	o, ok := t.d.tasks[v.ID]
	if !ok {
		return domain.NewError(domain.CodeNotFound, "task not found")
	}
	if o.Version != e {
		return domain.NewError(domain.CodeConflict, "task stale")
	}
	t.d.tasks[v.ID] = v
	return nil
}
func invKey(tenant, task, key string) string { return tenant + "\x00" + task + "\x00" + key }
func (t *transaction) FindInvocation(tenant, task, key string) (domain.Invocation, bool) {
	for _, v := range t.d.invocations {
		if invKey(v.TenantID, v.TaskID, v.IdempotencyKey) == invKey(tenant, task, key) {
			v.Response = append([]byte(nil), v.Response...)
			return v, true
		}
	}
	return domain.Invocation{}, false
}
func (t *transaction) GetInvocation(id string) (domain.Invocation, bool) {
	v, ok := t.d.invocations[id]
	v.Response = append([]byte(nil), v.Response...)
	return v, ok
}
func (t *transaction) InsertInvocation(v domain.Invocation) error {
	if _, ok := t.d.invocations[v.ID]; ok {
		return domain.NewError(domain.CodeConflict, "invocation exists")
	}
	if _, ok := t.FindInvocation(v.TenantID, v.TaskID, v.IdempotencyKey); ok {
		return domain.NewError(domain.CodeConflict, "idempotency key exists")
	}
	t.d.invocations[v.ID] = v
	return nil
}
func (t *transaction) UpdateInvocation(v domain.Invocation, e int64) error {
	o, ok := t.d.invocations[v.ID]
	if !ok {
		return domain.NewError(domain.CodeNotFound, "invocation not found")
	}
	if o.Version != e {
		return domain.NewError(domain.CodeConflict, "invocation stale")
	}
	if o.TenantID != v.TenantID || o.AgentID != v.AgentID || o.TaskID != v.TaskID || o.Tool != v.Tool || o.IdempotencyKey != v.IdempotencyKey || o.Fingerprint != v.Fingerprint {
		return domain.NewError(domain.CodeConflict, "invocation identity immutable")
	}
	t.d.invocations[v.ID] = v
	return nil
}
func (t *transaction) GetApprovalRequest(id string) (domain.ApprovalRequest, bool) {
	v, ok := t.d.approvals[id]
	return v, ok
}
func (t *transaction) InsertApprovalRequest(v domain.ApprovalRequest) error {
	if _, ok := t.d.approvals[v.ID]; ok {
		return domain.NewError(domain.CodeConflict, "approval exists")
	}
	t.d.approvals[v.ID] = v
	return nil
}
func (t *transaction) UpdateApprovalRequest(v domain.ApprovalRequest, e int64) error {
	o, ok := t.d.approvals[v.ID]
	if !ok {
		return domain.NewError(domain.CodeNotFound, "approval not found")
	}
	if o.Version != e {
		return domain.NewError(domain.CodeConflict, "approval stale")
	}
	if o.TenantID != v.TenantID || o.AgentID != v.AgentID || o.TaskID != v.TaskID || o.Action != v.Action || o.Resource != v.Resource || o.PayloadHash != v.PayloadHash {
		return domain.NewError(domain.CodeConflict, "approval identity immutable")
	}
	t.d.approvals[v.ID] = v
	return nil
}
func (t *transaction) GetApprovalGrant(id string) (domain.ApprovalGrant, bool) {
	v, ok := t.d.grants[id]
	if v.ConsumedAt != nil {
		x := *v.ConsumedAt
		v.ConsumedAt = &x
	}
	return v, ok
}
func (t *transaction) InsertApprovalGrant(v domain.ApprovalGrant) error {
	if _, ok := t.d.grants[v.ID]; ok {
		return domain.NewError(domain.CodeConflict, "grant exists")
	}
	for _, g := range t.d.grants {
		if g.RequestID == v.RequestID {
			return domain.NewError(domain.CodeConflict, "request already granted")
		}
	}
	t.d.grants[v.ID] = v
	return nil
}
func (t *transaction) UpdateApprovalGrant(v domain.ApprovalGrant, e int64) error {
	o, ok := t.d.grants[v.ID]
	if !ok {
		return domain.NewError(domain.CodeNotFound, "grant not found")
	}
	if o.Version != e {
		return domain.NewError(domain.CodeConflict, "grant stale")
	}
	if o.RequestID != v.RequestID || o.TenantID != v.TenantID || o.AgentID != v.AgentID || o.TaskID != v.TaskID || o.Action != v.Action || o.Resource != v.Resource || o.PayloadHash != v.PayloadHash {
		return domain.NewError(domain.CodeConflict, "grant identity immutable")
	}
	t.d.grants[v.ID] = v
	return nil
}
func (t *transaction) AppendAudit(v domain.AuditRecord) error {
	for _, a := range t.d.audit {
		if a.ID == v.ID {
			return domain.NewError(domain.CodeConflict, "audit exists")
		}
	}
	t.d.audit = append(t.d.audit, v)
	return nil
}
func (t *transaction) ListAudit(tenant, task string) []domain.AuditRecord {
	out := []domain.AuditRecord{}
	for _, v := range t.d.audit {
		if v.TenantID == tenant && v.TaskID == task {
			out = append(out, v)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out
}
func (t *transaction) AppendOutbox(v domain.OutboxRecord) error {
	for _, a := range t.d.outbox {
		if a.ID == v.ID {
			return domain.NewError(domain.CodeConflict, "outbox exists")
		}
	}
	v.Payload = append([]byte(nil), v.Payload...)
	t.d.outbox = append(t.d.outbox, v)
	return nil
}
func (s *Store) Counts() (principals, tasks, invocations, approvals, grants, audit, outbox int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.d.principals), len(s.d.tasks), len(s.d.invocations), len(s.d.approvals), len(s.d.grants), len(s.d.audit), len(s.d.outbox)
}
