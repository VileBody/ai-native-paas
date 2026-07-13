// Package v1 contains the stable public contracts exported by the platform kernel.
//
// The package intentionally has no dependencies on internal implementation packages.
package v1

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

type TenantID string
type PrincipalID string
type OperationID string
type CorrelationID string
type CausationID string
type IdempotencyKey string

type PrincipalKind string

const (
	PrincipalKindUser    PrincipalKind = "user"
	PrincipalKindAgent   PrincipalKind = "agent"
	PrincipalKindService PrincipalKind = "service"
)

func (k PrincipalKind) Valid() bool {
	switch k {
	case PrincipalKindUser, PrincipalKindAgent, PrincipalKindService:
		return true
	default:
		return false
	}
}

// PrincipalContext is the authenticated caller identity.
// TenantID is intentionally empty immediately after OIDC authentication: tenant
// context is derived by the application from the requested resource and active
// membership, never from an unsigned/request-provided claim.
type PrincipalContext struct {
	PrincipalID PrincipalID   `json:"principal_id"`
	TenantID    TenantID      `json:"tenant_id,omitempty"`
	Kind        PrincipalKind `json:"kind"`
	Scopes      []string      `json:"scopes"`
}

func (p PrincipalContext) Validate() error {
	if strings.TrimSpace(string(p.PrincipalID)) == "" {
		return errors.New("principal_id is required")
	}
	if !p.Kind.Valid() {
		return fmt.Errorf("invalid principal kind %q", p.Kind)
	}
	return nil
}

// WithTenant derives a tenant-scoped principal after the application has
// resolved and authorized an active membership.
func (p PrincipalContext) WithTenant(id TenantID) PrincipalContext {
	p.TenantID = id
	p.Scopes = append([]string(nil), p.Scopes...)
	return p
}

// HasScope supports exact scopes, the global wildcard, and prefix wildcards
// such as "kernel:*".
func (p PrincipalContext) HasScope(action string) bool {
	for _, scope := range p.Scopes {
		scope = strings.TrimSpace(scope)
		if scope == "*" || scope == action {
			return true
		}
		if strings.HasSuffix(scope, "*") {
			prefix := strings.TrimSuffix(scope, "*")
			if strings.HasPrefix(action, prefix) {
				return true
			}
			// OAuth scopes commonly use "kernel:*" while internal actions use
			// dotted names such as "kernel.organization.read".
			if strings.HasSuffix(prefix, ":") && strings.HasPrefix(action, strings.TrimSuffix(prefix, ":")+".") {
				return true
			}
		}
	}
	return false
}

type TenantRef struct {
	TenantID TenantID `json:"tenant_id"`
}

type ResourceRef struct {
	TenantID TenantID `json:"tenant_id"`
	Type     string   `json:"type"`
	ID       string   `json:"id"`
}

func (r ResourceRef) Validate() error {
	if strings.TrimSpace(string(r.TenantID)) == "" {
		return errors.New("resource tenant_id is required")
	}
	if strings.TrimSpace(r.Type) == "" || strings.TrimSpace(r.ID) == "" {
		return errors.New("resource type and id are required")
	}
	return nil
}

type OperationState string

const (
	OperationPending         OperationState = "PENDING"
	OperationRunning         OperationState = "RUNNING"
	OperationWaitingExternal OperationState = "WAITING_EXTERNAL"
	OperationSucceeded       OperationState = "SUCCEEDED"
	OperationFailed          OperationState = "FAILED"
	OperationCanceled        OperationState = "CANCELED"
)

func (s OperationState) Terminal() bool {
	return s == OperationSucceeded || s == OperationFailed || s == OperationCanceled
}

type OperationRef struct {
	OperationID OperationID    `json:"operation_id"`
	TenantID    TenantID       `json:"tenant_id,omitempty"`
	State       OperationState `json:"state"`
}

type OperationResult struct {
	Code  string          `json:"code,omitempty"`
	Data  json.RawMessage `json:"data,omitempty"`
	Error *PublicError    `json:"error,omitempty"`
}

type OperationSnapshot struct {
	OperationRef
	Kind          string          `json:"kind"`
	CorrelationID CorrelationID   `json:"correlation_id"`
	CausationID   CausationID     `json:"causation_id,omitempty"`
	Result        OperationResult `json:"result"`
	Version       int64           `json:"version"`
	CreatedAt     time.Time       `json:"created_at"`
	UpdatedAt     time.Time       `json:"updated_at"`
}

type CommandMeta struct {
	TenantID       TenantID         `json:"tenant_id,omitempty"`
	Principal      PrincipalContext `json:"principal"`
	CorrelationID  CorrelationID    `json:"correlation_id"`
	CausationID    CausationID      `json:"causation_id,omitempty"`
	IdempotencyKey IdempotencyKey   `json:"idempotency_key"`
	Command        string           `json:"command"`
}

func (m CommandMeta) Validate() error {
	if err := m.Principal.Validate(); err != nil {
		return err
	}
	if strings.TrimSpace(string(m.CorrelationID)) == "" {
		return errors.New("correlation_id is required")
	}
	if strings.TrimSpace(string(m.IdempotencyKey)) == "" {
		return errors.New("idempotency_key is required")
	}
	if strings.TrimSpace(m.Command) == "" {
		return errors.New("command is required")
	}
	return nil
}

type PublicError struct {
	Code        string         `json:"code"`
	Message     string         `json:"message"`
	OperationID OperationID    `json:"operation_id,omitempty"`
	Retryable   bool           `json:"retryable"`
	Details     map[string]any `json:"details,omitempty"`
}

func (e PublicError) Error() string { return e.Message }

// DomainEventEnvelope is the immutable event contract emitted through outbox.
type DomainEventEnvelope[T any] struct {
	EventID       string        `json:"event_id"`
	Type          string        `json:"type"`
	Version       int           `json:"version"`
	TenantID      TenantID      `json:"tenant_id,omitempty"`
	AggregateID   string        `json:"aggregate_id"`
	CorrelationID CorrelationID `json:"correlation_id"`
	CausationID   CausationID   `json:"causation_id,omitempty"`
	OccurredAt    time.Time     `json:"occurred_at"`
	Payload       T             `json:"payload"`
}

func (e DomainEventEnvelope[T]) Validate() error {
	switch {
	case strings.TrimSpace(e.EventID) == "":
		return errors.New("event_id is required")
	case strings.TrimSpace(e.Type) == "":
		return errors.New("event type is required")
	case e.Version <= 0:
		return errors.New("event version must be positive")
	case strings.TrimSpace(e.AggregateID) == "":
		return errors.New("aggregate_id is required")
	case strings.TrimSpace(string(e.CorrelationID)) == "":
		return errors.New("correlation_id is required")
	case e.OccurredAt.IsZero():
		return errors.New("occurred_at is required")
	default:
		return nil
	}
}

type AuditOutcome string

const (
	AuditOutcomeSucceeded AuditOutcome = "SUCCEEDED"
	AuditOutcomeDenied    AuditOutcome = "DENIED"
	AuditOutcomeFailed    AuditOutcome = "FAILED"
)

type AuditEnvelope struct {
	AuditID       string           `json:"audit_id"`
	TenantID      TenantID         `json:"tenant_id,omitempty"`
	Actor         PrincipalContext `json:"actor"`
	Action        string           `json:"action"`
	Resource      ResourceRef      `json:"resource"`
	CorrelationID CorrelationID    `json:"correlation_id"`
	Outcome       AuditOutcome     `json:"outcome"`
	ErrorCode     string           `json:"error_code,omitempty"`
	Metadata      map[string]any   `json:"metadata,omitempty"`
	OccurredAt    time.Time        `json:"occurred_at"`
}

type Authorizer interface {
	Check(ctx context.Context, principal PrincipalContext, action string, resource ResourceRef) error
}

type OperationService interface {
	Start(ctx context.Context, cmd CommandMeta) (OperationRef, error)
	Transition(ctx context.Context, id OperationID, to OperationState, result OperationResult) error
	Get(ctx context.Context, id OperationID) (OperationSnapshot, error)
}

type EventPublisher interface {
	Publish(ctx context.Context, event DomainEventEnvelope[json.RawMessage]) error
}

// MarshalJSON defensively redacts values stored under sensitive keys before an
// event crosses a process boundary. Domain producers must still avoid placing
// credentials in events; this is a final safety net, not a secret transport.
func (e DomainEventEnvelope[T]) MarshalJSON() ([]byte, error) {
	type envelope struct {
		EventID       string        `json:"event_id"`
		Type          string        `json:"type"`
		Version       int           `json:"version"`
		TenantID      TenantID      `json:"tenant_id,omitempty"`
		AggregateID   string        `json:"aggregate_id"`
		CorrelationID CorrelationID `json:"correlation_id"`
		CausationID   CausationID   `json:"causation_id,omitempty"`
		OccurredAt    time.Time     `json:"occurred_at"`
		Payload       any           `json:"payload"`
	}
	payloadBytes, err := json.Marshal(e.Payload)
	if err != nil {
		return nil, err
	}
	var payload any
	if err := json.Unmarshal(payloadBytes, &payload); err != nil {
		return nil, err
	}
	return json.Marshal(envelope{
		EventID:       e.EventID,
		Type:          e.Type,
		Version:       e.Version,
		TenantID:      e.TenantID,
		AggregateID:   e.AggregateID,
		CorrelationID: e.CorrelationID,
		CausationID:   e.CausationID,
		OccurredAt:    e.OccurredAt,
		Payload:       redactContractValue(payload, ""),
	})
}

func redactContractValue(value any, key string) any {
	if contractSensitiveKey(key) {
		return "[REDACTED]"
	}
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for childKey, childValue := range typed {
			out[childKey] = redactContractValue(childValue, childKey)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for i, child := range typed {
			out[i] = redactContractValue(child, key)
		}
		return out
	default:
		return value
	}
}

func contractSensitiveKey(key string) bool {
	normalized := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(key), "-", "_"))
	fragments := [...]string{"password", "passwd", "secret", "token", "authorization", "credential", "api_key", "apikey", "private_key", "access_key"}
	for _, fragment := range fragments {
		if strings.Contains(normalized, fragment) {
			return true
		}
	}
	return false
}
