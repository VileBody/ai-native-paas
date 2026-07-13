package kernel

import (
	"context"
	"encoding/json"
	"time"

	kernelv1 "github.com/keir-research/ai-native-paas/contracts/kernel/v1"
)

type IdempotencyStatus string

const (
	IdempotencyStarted   IdempotencyStatus = "STARTED"
	IdempotencyCompleted IdempotencyStatus = "COMPLETED"
)

type IdempotencyRecord struct {
	Scope       string
	Key         kernelv1.IdempotencyKey
	Fingerprint string
	Status      IdempotencyStatus
	Response    json.RawMessage
	OperationID kernelv1.OperationID
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

func (r *IdempotencyRecord) Clone() *IdempotencyRecord {
	if r == nil {
		return nil
	}
	copy := *r
	copy.Response = append(json.RawMessage(nil), r.Response...)
	return &copy
}

type OutboxState string

const (
	OutboxPending     OutboxState = "PENDING"
	OutboxDispatching OutboxState = "DISPATCHING"
	OutboxPublished   OutboxState = "PUBLISHED"
)

type OutboxRecord struct {
	Event       kernelv1.DomainEventEnvelope[json.RawMessage]
	State       OutboxState
	Attempts    int
	LeaseUntil  *time.Time
	LastError   string
	CreatedAt   time.Time
	PublishedAt *time.Time
}

func (r *OutboxRecord) Clone() *OutboxRecord {
	if r == nil {
		return nil
	}
	copy := *r
	copy.Event.Payload = append(json.RawMessage(nil), r.Event.Payload...)
	if r.LeaseUntil != nil {
		t := *r.LeaseUntil
		copy.LeaseUntil = &t
	}
	if r.PublishedAt != nil {
		t := *r.PublishedAt
		copy.PublishedAt = &t
	}
	return &copy
}

type InboxRecord struct {
	EventID     string
	Fingerprint string
	ProcessedAt time.Time
}

func (r *InboxRecord) Clone() *InboxRecord {
	if r == nil {
		return nil
	}
	copy := *r
	return &copy
}

type Reader interface {
	GetOrganization(ctx context.Context, id kernelv1.TenantID) (*Organization, error)
	FindOrganizationBySlug(ctx context.Context, slug string) (*Organization, error)
	GetOperation(ctx context.Context, id kernelv1.OperationID) (*Operation, error)
	GetIdempotency(ctx context.Context, scope string, key kernelv1.IdempotencyKey) (*IdempotencyRecord, error)
	GetInbox(ctx context.Context, eventID string) (*InboxRecord, error)
	ListAudit(ctx context.Context, tenantID kernelv1.TenantID, limit int) ([]kernelv1.AuditEnvelope, error)
	ListOutbox(ctx context.Context) ([]OutboxRecord, error)
}

type Tx interface {
	Reader

	InsertOrganization(ctx context.Context, organization *Organization) error
	SaveOrganization(ctx context.Context, organization *Organization, expectedVersion int64) error

	InsertOperation(ctx context.Context, operation *Operation) error
	SaveOperation(ctx context.Context, operation *Operation, expectedVersion int64) error

	ClaimIdempotency(ctx context.Context, record IdempotencyRecord) (existing *IdempotencyRecord, claimed bool, err error)
	CompleteIdempotency(ctx context.Context, scope string, key kernelv1.IdempotencyKey, operationID kernelv1.OperationID, response json.RawMessage, now time.Time) error

	AppendOutbox(ctx context.Context, record OutboxRecord) error
	ClaimOutbox(ctx context.Context, limit int, now time.Time, lease time.Duration) ([]OutboxRecord, error)
	MarkOutboxPublished(ctx context.Context, eventID string, now time.Time) error
	MarkOutboxPending(ctx context.Context, eventID, lastError string, now time.Time) error

	InsertInbox(ctx context.Context, record InboxRecord) error
	AppendAudit(ctx context.Context, record kernelv1.AuditEnvelope) error
}

type Store interface {
	Transact(ctx context.Context, fn func(Tx) error) error
	View(ctx context.Context, fn func(Reader) error) error
}
