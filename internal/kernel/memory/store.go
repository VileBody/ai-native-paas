// Package memory provides a concurrency-safe, copy-on-write transactional store
// used by unit tests, acceptance tests, and local development.
package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"time"

	kernelv1 "github.com/keir-research/ai-native-paas/contracts/kernel/v1"
	"github.com/keir-research/ai-native-paas/internal/kernel"
)

type Store struct {
	mu    sync.RWMutex
	state *state
}

type state struct {
	organizations map[kernelv1.TenantID]*kernel.Organization
	slugIndex     map[string]kernelv1.TenantID
	operations    map[kernelv1.OperationID]*kernel.Operation
	idempotency   map[string]*kernel.IdempotencyRecord
	outbox        map[string]*kernel.OutboxRecord
	outboxOrder   []string
	inbox         map[string]*kernel.InboxRecord
	audit         []kernelv1.AuditEnvelope
}

func NewStore() *Store {
	return &Store{state: newState()}
}

func newState() *state {
	return &state{
		organizations: make(map[kernelv1.TenantID]*kernel.Organization),
		slugIndex:     make(map[string]kernelv1.TenantID),
		operations:    make(map[kernelv1.OperationID]*kernel.Operation),
		idempotency:   make(map[string]*kernel.IdempotencyRecord),
		outbox:        make(map[string]*kernel.OutboxRecord),
		inbox:         make(map[string]*kernel.InboxRecord),
	}
}

func (s *state) clone() *state {
	clone := newState()
	for id, organization := range s.organizations {
		clone.organizations[id] = organization.Clone()
	}
	for slug, id := range s.slugIndex {
		clone.slugIndex[slug] = id
	}
	for id, operation := range s.operations {
		clone.operations[id] = operation.Clone()
	}
	for key, record := range s.idempotency {
		clone.idempotency[key] = record.Clone()
	}
	for id, record := range s.outbox {
		clone.outbox[id] = record.Clone()
	}
	clone.outboxOrder = append([]string(nil), s.outboxOrder...)
	for id, record := range s.inbox {
		clone.inbox[id] = record.Clone()
	}
	clone.audit = make([]kernelv1.AuditEnvelope, len(s.audit))
	for i, record := range s.audit {
		clone.audit[i] = cloneAudit(record)
	}
	return clone
}

func (s *Store) Transact(ctx context.Context, fn func(kernel.Tx) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	working := s.state.clone()
	tx := &transaction{state: working}
	if err := fn(tx); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	s.state = working
	return nil
}

func (s *Store) View(ctx context.Context, fn func(kernel.Reader) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return fn(&transaction{state: s.state})
}

type transaction struct {
	state *state
}

func (tx *transaction) GetOrganization(_ context.Context, id kernelv1.TenantID) (*kernel.Organization, error) {
	organization, ok := tx.state.organizations[id]
	if !ok {
		return nil, kernel.NewError(kernelv1.CodeNotFound, "organization not found")
	}
	return organization.Clone(), nil
}

func (tx *transaction) FindOrganizationBySlug(_ context.Context, slug string) (*kernel.Organization, error) {
	id, ok := tx.state.slugIndex[slug]
	if !ok {
		return nil, kernel.NewError(kernelv1.CodeNotFound, "organization not found")
	}
	return tx.state.organizations[id].Clone(), nil
}

func (tx *transaction) InsertOrganization(_ context.Context, organization *kernel.Organization) error {
	if organization == nil {
		return kernel.NewError(kernelv1.CodeInvalidArgument, "organization is required")
	}
	if _, exists := tx.state.organizations[organization.ID]; exists {
		return kernel.NewError(kernelv1.CodeConflict, "organization already exists")
	}
	if _, exists := tx.state.slugIndex[organization.Slug]; exists {
		return kernel.NewError(kernelv1.CodeConflict, "organization slug already exists")
	}
	tx.state.organizations[organization.ID] = organization.Clone()
	tx.state.slugIndex[organization.Slug] = organization.ID
	return nil
}

func (tx *transaction) SaveOrganization(_ context.Context, organization *kernel.Organization, expectedVersion int64) error {
	if organization == nil {
		return kernel.NewError(kernelv1.CodeInvalidArgument, "organization is required")
	}
	existing, ok := tx.state.organizations[organization.ID]
	if !ok {
		return kernel.NewError(kernelv1.CodeNotFound, "organization not found")
	}
	if existing.Version != expectedVersion || organization.Version < expectedVersion {
		return kernel.NewError(kernelv1.CodeOptimisticLock, "Concurrent modification detected")
	}
	if owner, exists := tx.state.slugIndex[organization.Slug]; exists && owner != organization.ID {
		return kernel.NewError(kernelv1.CodeConflict, "organization slug already exists")
	}
	if existing.Slug != organization.Slug {
		delete(tx.state.slugIndex, existing.Slug)
		tx.state.slugIndex[organization.Slug] = organization.ID
	}
	tx.state.organizations[organization.ID] = organization.Clone()
	return nil
}

func (tx *transaction) GetOperation(_ context.Context, id kernelv1.OperationID) (*kernel.Operation, error) {
	operation, ok := tx.state.operations[id]
	if !ok {
		return nil, kernel.NewError(kernelv1.CodeNotFound, "operation not found")
	}
	return operation.Clone(), nil
}

func (tx *transaction) InsertOperation(_ context.Context, operation *kernel.Operation) error {
	if operation == nil {
		return kernel.NewError(kernelv1.CodeInvalidArgument, "operation is required")
	}
	if _, exists := tx.state.operations[operation.ID]; exists {
		return kernel.NewError(kernelv1.CodeConflict, "operation already exists")
	}
	tx.state.operations[operation.ID] = operation.Clone()
	return nil
}

func (tx *transaction) SaveOperation(_ context.Context, operation *kernel.Operation, expectedVersion int64) error {
	if operation == nil {
		return kernel.NewError(kernelv1.CodeInvalidArgument, "operation is required")
	}
	existing, ok := tx.state.operations[operation.ID]
	if !ok {
		return kernel.NewError(kernelv1.CodeNotFound, "operation not found")
	}
	if existing.Version != expectedVersion || operation.Version < expectedVersion {
		return kernel.NewError(kernelv1.CodeOptimisticLock, "Concurrent modification detected")
	}
	tx.state.operations[operation.ID] = operation.Clone()
	return nil
}

func idempotencyMapKey(scope string, key kernelv1.IdempotencyKey) string {
	return scope + "\x00" + string(key)
}

func (tx *transaction) GetIdempotency(_ context.Context, scope string, key kernelv1.IdempotencyKey) (*kernel.IdempotencyRecord, error) {
	record, ok := tx.state.idempotency[idempotencyMapKey(scope, key)]
	if !ok {
		return nil, kernel.NewError(kernelv1.CodeNotFound, "idempotency record not found")
	}
	return record.Clone(), nil
}

func (tx *transaction) ClaimIdempotency(_ context.Context, record kernel.IdempotencyRecord) (*kernel.IdempotencyRecord, bool, error) {
	mapKey := idempotencyMapKey(record.Scope, record.Key)
	if existing, ok := tx.state.idempotency[mapKey]; ok {
		return existing.Clone(), false, nil
	}
	if record.Scope == "" || record.Key == "" || record.Fingerprint == "" {
		return nil, false, kernel.NewError(kernelv1.CodeInvalidArgument, "invalid idempotency record")
	}
	record.Status = kernel.IdempotencyStarted
	tx.state.idempotency[mapKey] = record.Clone()
	return nil, true, nil
}

func (tx *transaction) CompleteIdempotency(_ context.Context, scope string, key kernelv1.IdempotencyKey, operationID kernelv1.OperationID, response json.RawMessage, now time.Time) error {
	record, ok := tx.state.idempotency[idempotencyMapKey(scope, key)]
	if !ok {
		return kernel.NewError(kernelv1.CodeNotFound, "idempotency record not found")
	}
	if record.Status == kernel.IdempotencyCompleted {
		if record.OperationID == operationID && string(record.Response) == string(response) {
			return nil
		}
		return kernel.NewError(kernelv1.CodeConflict, "idempotency record is already completed")
	}
	record.Status = kernel.IdempotencyCompleted
	record.OperationID = operationID
	record.Response = append(json.RawMessage(nil), response...)
	record.UpdatedAt = now.UTC()
	return nil
}

func (tx *transaction) AppendOutbox(_ context.Context, record kernel.OutboxRecord) error {
	eventID := record.Event.EventID
	if eventID == "" {
		return kernel.NewError(kernelv1.CodeInvalidArgument, "outbox event id is required")
	}
	if _, exists := tx.state.outbox[eventID]; exists {
		return kernel.NewError(kernelv1.CodeConflict, "outbox event already exists")
	}
	if record.State == "" {
		record.State = kernel.OutboxPending
	}
	tx.state.outbox[eventID] = record.Clone()
	tx.state.outboxOrder = append(tx.state.outboxOrder, eventID)
	return nil
}

func (tx *transaction) ClaimOutbox(_ context.Context, limit int, now time.Time, lease time.Duration) ([]kernel.OutboxRecord, error) {
	if limit <= 0 {
		return nil, nil
	}
	claimed := make([]kernel.OutboxRecord, 0, limit)
	for _, eventID := range tx.state.outboxOrder {
		if len(claimed) == limit {
			break
		}
		record := tx.state.outbox[eventID]
		eligible := record.State == kernel.OutboxPending
		if record.State == kernel.OutboxDispatching && record.LeaseUntil != nil && !record.LeaseUntil.After(now) {
			eligible = true
		}
		if !eligible {
			continue
		}
		leaseUntil := now.Add(lease).UTC()
		record.State = kernel.OutboxDispatching
		record.LeaseUntil = &leaseUntil
		claimed = append(claimed, *record.Clone())
	}
	return claimed, nil
}

func (tx *transaction) MarkOutboxPublished(_ context.Context, eventID string, now time.Time) error {
	record, ok := tx.state.outbox[eventID]
	if !ok {
		return kernel.NewError(kernelv1.CodeNotFound, "outbox event not found")
	}
	if record.State == kernel.OutboxPublished {
		return nil
	}
	published := now.UTC()
	record.State = kernel.OutboxPublished
	record.PublishedAt = &published
	record.LeaseUntil = nil
	record.LastError = ""
	return nil
}

func (tx *transaction) MarkOutboxPending(_ context.Context, eventID, lastError string, _ time.Time) error {
	record, ok := tx.state.outbox[eventID]
	if !ok {
		return kernel.NewError(kernelv1.CodeNotFound, "outbox event not found")
	}
	if record.State == kernel.OutboxPublished {
		return kernel.NewError(kernelv1.CodeConflict, "published outbox event cannot return to pending")
	}
	record.State = kernel.OutboxPending
	record.Attempts++
	record.LeaseUntil = nil
	record.LastError = lastError
	return nil
}

func (tx *transaction) ListOutbox(_ context.Context) ([]kernel.OutboxRecord, error) {
	records := make([]kernel.OutboxRecord, 0, len(tx.state.outboxOrder))
	for _, eventID := range tx.state.outboxOrder {
		records = append(records, *tx.state.outbox[eventID].Clone())
	}
	return records, nil
}

func (tx *transaction) GetInbox(_ context.Context, eventID string) (*kernel.InboxRecord, error) {
	record, ok := tx.state.inbox[eventID]
	if !ok {
		return nil, kernel.NewError(kernelv1.CodeNotFound, "inbox event not found")
	}
	return record.Clone(), nil
}

func (tx *transaction) InsertInbox(_ context.Context, record kernel.InboxRecord) error {
	if record.EventID == "" || record.Fingerprint == "" {
		return kernel.NewError(kernelv1.CodeInvalidArgument, "invalid inbox record")
	}
	if _, exists := tx.state.inbox[record.EventID]; exists {
		return kernel.NewError(kernelv1.CodeConflict, "inbox event already exists")
	}
	tx.state.inbox[record.EventID] = record.Clone()
	return nil
}

func (tx *transaction) AppendAudit(_ context.Context, record kernelv1.AuditEnvelope) error {
	if record.AuditID == "" || record.Action == "" || record.CorrelationID == "" || record.OccurredAt.IsZero() {
		return kernel.NewError(kernelv1.CodeInvalidArgument, "invalid audit record")
	}
	for _, existing := range tx.state.audit {
		if existing.AuditID == record.AuditID {
			return kernel.NewError(kernelv1.CodeConflict, "audit record already exists")
		}
	}
	tx.state.audit = append(tx.state.audit, cloneAudit(record))
	return nil
}

func (tx *transaction) ListAudit(_ context.Context, tenantID kernelv1.TenantID, limit int) ([]kernelv1.AuditEnvelope, error) {
	if limit <= 0 {
		limit = 100
	}
	records := make([]kernelv1.AuditEnvelope, 0, limit)
	for _, record := range tx.state.audit {
		if tenantID != "" && record.TenantID != tenantID {
			continue
		}
		records = append(records, cloneAudit(record))
	}
	sort.SliceStable(records, func(i, j int) bool {
		if records[i].OccurredAt.Equal(records[j].OccurredAt) {
			return records[i].AuditID < records[j].AuditID
		}
		return records[i].OccurredAt.Before(records[j].OccurredAt)
	})
	if len(records) > limit {
		records = records[len(records)-limit:]
	}
	return records, nil
}

func cloneAudit(record kernelv1.AuditEnvelope) kernelv1.AuditEnvelope {
	encoded, err := json.Marshal(record.Metadata)
	if err != nil {
		panic(fmt.Sprintf("memory audit clone failed: %v", err))
	}
	var metadata map[string]any
	if len(encoded) > 0 && string(encoded) != "null" {
		if err := json.Unmarshal(encoded, &metadata); err != nil {
			panic(fmt.Sprintf("memory audit clone failed: %v", err))
		}
	}
	record.Metadata = metadata
	record.Actor.Scopes = append([]string(nil), record.Actor.Scopes...)
	return record
}

var _ kernel.Store = (*Store)(nil)
var _ kernel.Tx = (*transaction)(nil)
