// Package kernelpostgres implements the Platform Kernel persistence ports using
// PostgreSQL and database/sql. A driver is deliberately injected by the host
// application so the adapter is not coupled to pgx or lib/pq.
package kernelpostgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	kernelv1 "github.com/keir-research/ai-native-paas/contracts/kernel/v1"
	domain "github.com/keir-research/ai-native-paas/internal/kernel"
)

type Store struct {
	db *sql.DB
}

func NewStore(db *sql.DB) (*Store, error) {
	if db == nil {
		return nil, errors.New("postgres kernel store requires database")
	}
	return &Store{db: db}, nil
}

func (s *Store) Transact(ctx context.Context, fn func(domain.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return mapDatabaseError("begin transaction", err)
	}
	defer func() { _ = tx.Rollback() }()
	adapter := &transaction{queryer: tx}
	if err := fn(adapter); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return mapDatabaseError("commit transaction", err)
	}
	return nil
}

func (s *Store) View(ctx context.Context, fn func(domain.Reader) error) error {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted, ReadOnly: true})
	if err != nil {
		return mapDatabaseError("begin read transaction", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := fn(&transaction{queryer: tx}); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return mapDatabaseError("commit read transaction", err)
	}
	return nil
}

type queryer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

type transaction struct {
	queryer queryer
}

func (tx *transaction) GetOrganization(ctx context.Context, id kernelv1.TenantID) (*domain.Organization, error) {
	return tx.getOrganization(ctx, `WHERE id = $1`, id)
}

func (tx *transaction) FindOrganizationBySlug(ctx context.Context, slug string) (*domain.Organization, error) {
	return tx.getOrganization(ctx, `WHERE slug = $1`, slug)
}

func (tx *transaction) getOrganization(ctx context.Context, predicate string, argument any) (*domain.Organization, error) {
	row := tx.queryer.QueryRowContext(ctx, `
        SELECT id, name, slug, version, created_at, updated_at
        FROM kernel.organizations `+predicate, argument)
	organization := &domain.Organization{Memberships: make(map[kernelv1.PrincipalID]*domain.Membership)}
	if err := row.Scan(&organization.ID, &organization.Name, &organization.Slug, &organization.Version, &organization.CreatedAt, &organization.UpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, domain.NewError(kernelv1.CodeNotFound, "organization not found")
		}
		return nil, mapDatabaseError("read organization", err)
	}
	rows, err := tx.queryer.QueryContext(ctx, `
        SELECT principal_id, role, state, invited_at, accepted_at, updated_at, version
        FROM kernel.memberships
        WHERE organization_id = $1
        ORDER BY principal_id`, organization.ID)
	if err != nil {
		return nil, mapDatabaseError("read memberships", err)
	}
	defer rows.Close()
	for rows.Next() {
		membership := &domain.Membership{}
		var acceptedAt sql.NullTime
		if err := rows.Scan(&membership.PrincipalID, &membership.Role, &membership.State, &membership.InvitedAt, &acceptedAt, &membership.UpdatedAt, &membership.Version); err != nil {
			return nil, mapDatabaseError("scan membership", err)
		}
		if acceptedAt.Valid {
			accepted := acceptedAt.Time.UTC()
			membership.AcceptedAt = &accepted
		}
		organization.Memberships[membership.PrincipalID] = membership
	}
	if err := rows.Err(); err != nil {
		return nil, mapDatabaseError("iterate memberships", err)
	}
	return organization, nil
}

func (tx *transaction) InsertOrganization(ctx context.Context, organization *domain.Organization) error {
	if organization == nil {
		return domain.NewError(kernelv1.CodeInvalidArgument, "organization is required")
	}
	if _, err := tx.queryer.ExecContext(ctx, `
        INSERT INTO kernel.organizations(id, name, slug, version, created_at, updated_at)
        VALUES ($1, $2, $3, $4, $5, $6)`,
		organization.ID, organization.Name, organization.Slug, organization.Version, organization.CreatedAt, organization.UpdatedAt,
	); err != nil {
		return mapDatabaseError("insert organization", err)
	}
	for _, membership := range organization.Memberships {
		if err := tx.upsertMembership(ctx, organization.ID, membership); err != nil {
			return err
		}
	}
	return nil
}

func (tx *transaction) SaveOrganization(ctx context.Context, organization *domain.Organization, expectedVersion int64) error {
	if organization == nil {
		return domain.NewError(kernelv1.CodeInvalidArgument, "organization is required")
	}
	result, err := tx.queryer.ExecContext(ctx, `
        UPDATE kernel.organizations
        SET name = $2, slug = $3, version = $4, updated_at = $5
        WHERE id = $1 AND version = $6`,
		organization.ID, organization.Name, organization.Slug, organization.Version, organization.UpdatedAt, expectedVersion,
	)
	if err != nil {
		return mapDatabaseError("update organization", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return mapDatabaseError("inspect organization update", err)
	}
	if affected != 1 {
		return domain.NewError(kernelv1.CodeOptimisticLock, "Concurrent modification detected")
	}
	for _, membership := range organization.Memberships {
		if err := tx.upsertMembership(ctx, organization.ID, membership); err != nil {
			return err
		}
	}
	return nil
}

func (tx *transaction) upsertMembership(ctx context.Context, organizationID kernelv1.TenantID, membership *domain.Membership) error {
	var acceptedAt any
	if membership.AcceptedAt != nil {
		acceptedAt = membership.AcceptedAt.UTC()
	}
	_, err := tx.queryer.ExecContext(ctx, `
        INSERT INTO kernel.memberships(
            organization_id, principal_id, role, state, invited_at, accepted_at, updated_at, version
        ) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
        ON CONFLICT (organization_id, principal_id) DO UPDATE SET
            role = EXCLUDED.role,
            state = EXCLUDED.state,
            invited_at = EXCLUDED.invited_at,
            accepted_at = EXCLUDED.accepted_at,
            updated_at = EXCLUDED.updated_at,
            version = EXCLUDED.version`,
		organizationID, membership.PrincipalID, membership.Role, membership.State,
		membership.InvitedAt, acceptedAt, membership.UpdatedAt, membership.Version,
	)
	if err != nil {
		return mapDatabaseError("upsert membership", err)
	}
	return nil
}

func (tx *transaction) GetOperation(ctx context.Context, id kernelv1.OperationID) (*domain.Operation, error) {
	operation := &domain.Operation{}
	var resultJSON []byte
	err := tx.queryer.QueryRowContext(ctx, `
        SELECT id, tenant_id, kind, state, correlation_id, causation_id, result, version, created_at, updated_at
        FROM kernel.operations
        WHERE id = $1`, id).Scan(
		&operation.ID, &operation.TenantID, &operation.Kind, &operation.State,
		&operation.CorrelationID, &operation.CausationID, &resultJSON,
		&operation.Version, &operation.CreatedAt, &operation.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, domain.NewError(kernelv1.CodeNotFound, "operation not found")
	}
	if err != nil {
		return nil, mapDatabaseError("read operation", err)
	}
	if len(resultJSON) > 0 {
		if err := json.Unmarshal(resultJSON, &operation.Result); err != nil {
			return nil, mapDatabaseError("decode operation result", err)
		}
	}
	return operation, nil
}

func (tx *transaction) InsertOperation(ctx context.Context, operation *domain.Operation) error {
	if operation == nil {
		return domain.NewError(kernelv1.CodeInvalidArgument, "operation is required")
	}
	resultJSON, err := json.Marshal(operation.Result)
	if err != nil {
		return domain.WrapError(kernelv1.CodeInternal, "Could not encode operation result", false, err)
	}
	_, err = tx.queryer.ExecContext(ctx, `
        INSERT INTO kernel.operations(
            id, tenant_id, kind, state, correlation_id, causation_id, result, version, created_at, updated_at
        ) VALUES ($1, $2, $3, $4, $5, $6, $7::jsonb, $8, $9, $10)`,
		operation.ID, operation.TenantID, operation.Kind, operation.State,
		operation.CorrelationID, operation.CausationID, string(resultJSON),
		operation.Version, operation.CreatedAt, operation.UpdatedAt,
	)
	if err != nil {
		return mapDatabaseError("insert operation", err)
	}
	return nil
}

func (tx *transaction) SaveOperation(ctx context.Context, operation *domain.Operation, expectedVersion int64) error {
	if operation == nil {
		return domain.NewError(kernelv1.CodeInvalidArgument, "operation is required")
	}
	resultJSON, err := json.Marshal(operation.Result)
	if err != nil {
		return domain.WrapError(kernelv1.CodeInternal, "Could not encode operation result", false, err)
	}
	result, err := tx.queryer.ExecContext(ctx, `
        UPDATE kernel.operations
        SET state = $2, result = $3::jsonb, version = $4, updated_at = $5
        WHERE id = $1 AND version = $6`,
		operation.ID, operation.State, string(resultJSON), operation.Version, operation.UpdatedAt, expectedVersion,
	)
	if err != nil {
		return mapDatabaseError("update operation", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return mapDatabaseError("inspect operation update", err)
	}
	if affected != 1 {
		return domain.NewError(kernelv1.CodeOptimisticLock, "Concurrent modification detected")
	}
	return nil
}

func (tx *transaction) GetIdempotency(ctx context.Context, scope string, key kernelv1.IdempotencyKey) (*domain.IdempotencyRecord, error) {
	record := &domain.IdempotencyRecord{}
	var response []byte
	err := tx.queryer.QueryRowContext(ctx, `
        SELECT scope, idempotency_key, fingerprint, status, COALESCE(response, 'null'::jsonb), operation_id, created_at, updated_at
        FROM kernel.idempotency_records
        WHERE scope = $1 AND idempotency_key = $2`, scope, key).Scan(
		&record.Scope, &record.Key, &record.Fingerprint, &record.Status, &response,
		&record.OperationID, &record.CreatedAt, &record.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, domain.NewError(kernelv1.CodeNotFound, "idempotency record not found")
	}
	if err != nil {
		return nil, mapDatabaseError("read idempotency record", err)
	}
	if string(response) != "null" {
		record.Response = append(json.RawMessage(nil), response...)
	}
	return record, nil
}

func (tx *transaction) ClaimIdempotency(ctx context.Context, record domain.IdempotencyRecord) (*domain.IdempotencyRecord, bool, error) {
	result, err := tx.queryer.ExecContext(ctx, `
        INSERT INTO kernel.idempotency_records(
            scope, idempotency_key, fingerprint, status, response, operation_id, created_at, updated_at
        ) VALUES ($1, $2, $3, 'STARTED', NULL, '', $4, $5)
        ON CONFLICT (scope, idempotency_key) DO NOTHING`,
		record.Scope, record.Key, record.Fingerprint, record.CreatedAt, record.UpdatedAt,
	)
	if err != nil {
		return nil, false, mapDatabaseError("claim idempotency key", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return nil, false, mapDatabaseError("inspect idempotency claim", err)
	}
	if affected == 1 {
		return nil, true, nil
	}
	existing, err := tx.GetIdempotency(ctx, record.Scope, record.Key)
	return existing, false, err
}

func (tx *transaction) CompleteIdempotency(ctx context.Context, scope string, key kernelv1.IdempotencyKey, operationID kernelv1.OperationID, response json.RawMessage, now time.Time) error {
	result, err := tx.queryer.ExecContext(ctx, `
        UPDATE kernel.idempotency_records
        SET status = 'COMPLETED', response = $3::jsonb, operation_id = $4, updated_at = $5
        WHERE scope = $1 AND idempotency_key = $2 AND status = 'STARTED'`,
		scope, key, string(response), operationID, now,
	)
	if err != nil {
		return mapDatabaseError("complete idempotency record", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return mapDatabaseError("inspect idempotency completion", err)
	}
	if affected != 1 {
		existing, readErr := tx.GetIdempotency(ctx, scope, key)
		if readErr == nil && existing.Status == domain.IdempotencyCompleted && existing.OperationID == operationID && string(existing.Response) == string(response) {
			return nil
		}
		return domain.NewError(kernelv1.CodeConflict, "idempotency record could not be completed")
	}
	return nil
}

func (tx *transaction) AppendOutbox(ctx context.Context, record domain.OutboxRecord) error {
	envelope, err := json.Marshal(record.Event)
	if err != nil {
		return domain.WrapError(kernelv1.CodeInternal, "Could not encode outbox event", false, err)
	}
	if record.State == "" {
		record.State = domain.OutboxPending
	}
	_, err = tx.queryer.ExecContext(ctx, `
        INSERT INTO kernel.outbox_events(event_id, envelope, state, attempts, lease_until, last_error, created_at, published_at)
        VALUES ($1, $2::jsonb, $3, $4, $5, $6, $7, $8)`,
		record.Event.EventID, string(envelope), record.State, record.Attempts,
		nullableTime(record.LeaseUntil), record.LastError, record.CreatedAt, nullableTime(record.PublishedAt),
	)
	if err != nil {
		return mapDatabaseError("append outbox event", err)
	}
	return nil
}

func (tx *transaction) ClaimOutbox(ctx context.Context, limit int, now time.Time, lease time.Duration) ([]domain.OutboxRecord, error) {
	if limit <= 0 {
		return nil, nil
	}
	rows, err := tx.queryer.QueryContext(ctx, `
        WITH candidates AS (
            SELECT event_id
            FROM kernel.outbox_events
            WHERE state = 'PENDING'
               OR (state = 'DISPATCHING' AND lease_until <= $1)
            ORDER BY created_at, event_id
            FOR UPDATE SKIP LOCKED
            LIMIT $2
        )
        UPDATE kernel.outbox_events AS outbox
        SET state = 'DISPATCHING', lease_until = $3
        FROM candidates
        WHERE outbox.event_id = candidates.event_id
        RETURNING outbox.envelope, outbox.state, outbox.attempts,
                  outbox.lease_until, outbox.last_error, outbox.created_at, outbox.published_at`,
		now, limit, now.Add(lease),
	)
	if err != nil {
		return nil, mapDatabaseError("claim outbox events", err)
	}
	defer rows.Close()
	records := make([]domain.OutboxRecord, 0, limit)
	for rows.Next() {
		record, err := scanOutboxRow(rows)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, mapDatabaseError("iterate claimed outbox events", err)
	}
	return records, nil
}

func (tx *transaction) MarkOutboxPublished(ctx context.Context, eventID string, now time.Time) error {
	result, err := tx.queryer.ExecContext(ctx, `
        UPDATE kernel.outbox_events
        SET state = 'PUBLISHED', lease_until = NULL, last_error = '', published_at = $2
        WHERE event_id = $1 AND state <> 'PUBLISHED'`, eventID, now)
	if err != nil {
		return mapDatabaseError("mark outbox event published", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return mapDatabaseError("inspect published event update", err)
	}
	if affected == 0 {
		var state string
		scanErr := tx.queryer.QueryRowContext(ctx, `SELECT state FROM kernel.outbox_events WHERE event_id = $1`, eventID).Scan(&state)
		if errors.Is(scanErr, sql.ErrNoRows) {
			return domain.NewError(kernelv1.CodeNotFound, "outbox event not found")
		}
		if scanErr != nil {
			return mapDatabaseError("read outbox event state", scanErr)
		}
		if state == string(domain.OutboxPublished) {
			return nil
		}
	}
	return nil
}

func (tx *transaction) MarkOutboxPending(ctx context.Context, eventID, lastError string, _ time.Time) error {
	result, err := tx.queryer.ExecContext(ctx, `
        UPDATE kernel.outbox_events
        SET state = 'PENDING', attempts = attempts + 1, lease_until = NULL, last_error = $2
        WHERE event_id = $1 AND state <> 'PUBLISHED'`, eventID, lastError)
	if err != nil {
		return mapDatabaseError("return outbox event to pending", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return mapDatabaseError("inspect pending event update", err)
	}
	if affected != 1 {
		return domain.NewError(kernelv1.CodeConflict, "outbox event cannot return to pending")
	}
	return nil
}

func (tx *transaction) ListOutbox(ctx context.Context) ([]domain.OutboxRecord, error) {
	rows, err := tx.queryer.QueryContext(ctx, `
        SELECT envelope, state, attempts, lease_until, last_error, created_at, published_at
        FROM kernel.outbox_events
        ORDER BY created_at, event_id`)
	if err != nil {
		return nil, mapDatabaseError("list outbox events", err)
	}
	defer rows.Close()
	records := make([]domain.OutboxRecord, 0)
	for rows.Next() {
		record, err := scanOutboxRow(rows)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, mapDatabaseError("iterate outbox events", err)
	}
	return records, nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanOutboxRow(row rowScanner) (domain.OutboxRecord, error) {
	var envelope []byte
	var leaseUntil, publishedAt sql.NullTime
	record := domain.OutboxRecord{}
	if err := row.Scan(&envelope, &record.State, &record.Attempts, &leaseUntil, &record.LastError, &record.CreatedAt, &publishedAt); err != nil {
		return domain.OutboxRecord{}, mapDatabaseError("scan outbox event", err)
	}
	if err := json.Unmarshal(envelope, &record.Event); err != nil {
		return domain.OutboxRecord{}, mapDatabaseError("decode outbox event", err)
	}
	if leaseUntil.Valid {
		value := leaseUntil.Time.UTC()
		record.LeaseUntil = &value
	}
	if publishedAt.Valid {
		value := publishedAt.Time.UTC()
		record.PublishedAt = &value
	}
	return record, nil
}

func (tx *transaction) GetInbox(ctx context.Context, eventID string) (*domain.InboxRecord, error) {
	record := &domain.InboxRecord{}
	err := tx.queryer.QueryRowContext(ctx, `
        SELECT event_id, fingerprint, processed_at
        FROM kernel.inbox_events
        WHERE event_id = $1`, eventID).Scan(&record.EventID, &record.Fingerprint, &record.ProcessedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, domain.NewError(kernelv1.CodeNotFound, "inbox event not found")
	}
	if err != nil {
		return nil, mapDatabaseError("read inbox event", err)
	}
	return record, nil
}

func (tx *transaction) InsertInbox(ctx context.Context, record domain.InboxRecord) error {
	_, err := tx.queryer.ExecContext(ctx, `
        INSERT INTO kernel.inbox_events(event_id, fingerprint, processed_at)
        VALUES ($1, $2, $3)`, record.EventID, record.Fingerprint, record.ProcessedAt)
	if err != nil {
		return mapDatabaseError("insert inbox event", err)
	}
	return nil
}

func (tx *transaction) AppendAudit(ctx context.Context, record kernelv1.AuditEnvelope) error {
	actor, err := json.Marshal(record.Actor)
	if err != nil {
		return domain.WrapError(kernelv1.CodeInternal, "Could not encode audit actor", false, err)
	}
	resource, err := json.Marshal(record.Resource)
	if err != nil {
		return domain.WrapError(kernelv1.CodeInternal, "Could not encode audit resource", false, err)
	}
	metadata, err := json.Marshal(domain.RedactMetadata(record.Metadata))
	if err != nil {
		return domain.WrapError(kernelv1.CodeInternal, "Could not encode audit metadata", false, err)
	}
	_, err = tx.queryer.ExecContext(ctx, `
        INSERT INTO kernel.audit_records(
            audit_id, tenant_id, actor, action, resource, correlation_id, outcome, error_code, metadata, occurred_at
        ) VALUES ($1, $2, $3::jsonb, $4, $5::jsonb, $6, $7, $8, $9::jsonb, $10)`,
		record.AuditID, record.TenantID, string(actor), record.Action, string(resource),
		record.CorrelationID, record.Outcome, record.ErrorCode, string(metadata), record.OccurredAt,
	)
	if err != nil {
		return mapDatabaseError("append audit record", err)
	}
	return nil
}

func (tx *transaction) ListAudit(ctx context.Context, tenantID kernelv1.TenantID, limit int) ([]kernelv1.AuditEnvelope, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := tx.queryer.QueryContext(ctx, `
        SELECT audit_id, tenant_id, actor, action, resource, correlation_id, outcome, error_code, metadata, occurred_at
        FROM kernel.audit_records
        WHERE ($1 = '' OR tenant_id = $1)
        ORDER BY occurred_at DESC, audit_id DESC
        LIMIT $2`, tenantID, limit)
	if err != nil {
		return nil, mapDatabaseError("list audit records", err)
	}
	defer rows.Close()
	descending := make([]kernelv1.AuditEnvelope, 0, limit)
	for rows.Next() {
		record := kernelv1.AuditEnvelope{}
		var actor, resource, metadata []byte
		if err := rows.Scan(
			&record.AuditID, &record.TenantID, &actor, &record.Action, &resource,
			&record.CorrelationID, &record.Outcome, &record.ErrorCode, &metadata, &record.OccurredAt,
		); err != nil {
			return nil, mapDatabaseError("scan audit record", err)
		}
		if err := json.Unmarshal(actor, &record.Actor); err != nil {
			return nil, mapDatabaseError("decode audit actor", err)
		}
		if err := json.Unmarshal(resource, &record.Resource); err != nil {
			return nil, mapDatabaseError("decode audit resource", err)
		}
		if err := json.Unmarshal(metadata, &record.Metadata); err != nil {
			return nil, mapDatabaseError("decode audit metadata", err)
		}
		descending = append(descending, record)
	}
	if err := rows.Err(); err != nil {
		return nil, mapDatabaseError("iterate audit records", err)
	}
	records := make([]kernelv1.AuditEnvelope, len(descending))
	for i := range descending {
		records[len(descending)-1-i] = descending[i]
	}
	return records, nil
}

func nullableTime(value *time.Time) any {
	if value == nil {
		return nil
	}
	return value.UTC()
}

func mapDatabaseError(operation string, err error) error {
	if err == nil {
		return nil
	}
	normalized := strings.ToLower(err.Error())
	switch {
	case strings.Contains(normalized, "duplicate key"), strings.Contains(normalized, "unique constraint"):
		return domain.NewError(kernelv1.CodeConflict, "Resource already exists")
	case strings.Contains(normalized, "serialization failure"), strings.Contains(normalized, "deadlock detected"):
		return domain.WrapError(kernelv1.CodeOptimisticLock, "Concurrent modification detected", true, err)
	default:
		return domain.WrapError(kernelv1.CodeInternal, fmt.Sprintf("Database operation %q failed", operation), true, err)
	}
}

var _ domain.Store = (*Store)(nil)
var _ domain.Tx = (*transaction)(nil)
