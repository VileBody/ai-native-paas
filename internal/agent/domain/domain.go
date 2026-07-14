package domain

import (
	"encoding/json"
	"errors"
	"fmt"
	agentv1 "github.com/keir-research/ai-native-paas/pkg/contracts/agent/v1"
	"strings"
	"time"
)

type Code string

const (
	CodeInvalidArgument   Code = "INVALID_ARGUMENT"
	CodePermissionDenied  Code = "PERMISSION_DENIED"
	CodeNotFound          Code = "NOT_FOUND"
	CodeConflict          Code = "CONFLICT"
	CodeBudgetExceeded    Code = "BUDGET_EXCEEDED"
	CodeApprovalRequired  Code = "APPROVAL_REQUIRED"
	CodePaused            Code = "AUTONOMY_PAUSED"
	CodeUnavailable       Code = "UNAVAILABLE"
	CodeEntitlementDenied Code = "ENTITLEMENT_DENIED"
	CodeCanceled          Code = "CANCELED"
)

type Error struct {
	Code        Code
	Message     string
	Retryable   bool
	OperationID string
	Cause       error
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}
func (e *Error) Unwrap() error                { return e.Cause }
func NewError(c Code, m string) *Error        { return &Error{Code: c, Message: m} }
func Wrap(c Code, m string, err error) *Error { return &Error{Code: c, Message: m, Cause: err} }
func Retryable(c Code, m, op string, err error) *Error {
	return &Error{Code: c, Message: m, Retryable: true, OperationID: op, Cause: err}
}
func AsError(err error) *Error {
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	return &Error{Code: CodeUnavailable, Message: "operation failed", Cause: err}
}

type PrincipalState string

const (
	PrincipalActive  PrincipalState = "ACTIVE"
	PrincipalRevoked PrincipalState = "REVOKED"
)

type AgentPrincipal struct {
	ID                  string         `json:"id"`
	TenantID            string         `json:"tenant_id"`
	OnBehalfOfUserID    string         `json:"on_behalf_of_user_id"`
	Scopes              []string       `json:"scopes"`
	State               PrincipalState `json:"state"`
	CredentialExpiresAt time.Time      `json:"credential_expires_at"`
	Version             int64          `json:"version"`
	CreatedAt           time.Time      `json:"created_at"`
	UpdatedAt           time.Time      `json:"updated_at"`
}

func (p AgentPrincipal) HasScope(scope string) bool {
	for _, s := range p.Scopes {
		if s == "*" || s == scope {
			return true
		}
	}
	return false
}
func (p AgentPrincipal) Validate() error {
	if !agentv1.ValidID(p.ID) || !agentv1.ValidID(p.TenantID) || !agentv1.ValidID(p.OnBehalfOfUserID) || p.State != PrincipalActive || p.Version < 1 {
		return errors.New("invalid agent principal")
	}
	return nil
}

type TaskState string

const (
	TaskActive    TaskState = "ACTIVE"
	TaskPaused    TaskState = "PAUSED"
	TaskCompleted TaskState = "COMPLETED"
	TaskCanceled  TaskState = "CANCELED"
)

type AgentTask struct {
	ID                string               `json:"id"`
	TenantID          string               `json:"tenant_id"`
	ProjectID         string               `json:"project_id,omitempty"`
	AgentID           string               `json:"agent_id"`
	OnBehalfOfUserID  string               `json:"on_behalf_of_user_id"`
	CorrelationID     string               `json:"correlation_id"`
	State             TaskState            `json:"state"`
	BudgetPolicy      agentv1.BudgetPolicy `json:"budget_policy"`
	BudgetUsage       agentv1.BudgetUsage  `json:"budget_usage"`
	RepairFingerprint string               `json:"repair_fingerprint,omitempty"`
	RepairCount       int64                `json:"repair_count"`
	Version           int64                `json:"version"`
	CreatedAt         time.Time            `json:"created_at"`
	UpdatedAt         time.Time            `json:"updated_at"`
}

func (t AgentTask) View() agentv1.TaskView {
	return agentv1.TaskView{TaskID: t.ID, TenantID: t.TenantID, ProjectID: t.ProjectID, AgentID: t.AgentID, OnBehalfOfUserID: t.OnBehalfOfUserID, CorrelationID: t.CorrelationID, State: string(t.State), BudgetPolicy: t.BudgetPolicy, BudgetUsage: t.BudgetUsage, RepairFingerprint: t.RepairFingerprint, RepairCount: t.RepairCount}
}

type ApprovalState string

const (
	ApprovalPending  ApprovalState = "PENDING"
	ApprovalGranted  ApprovalState = "GRANTED"
	ApprovalDenied   ApprovalState = "DENIED"
	ApprovalExpired  ApprovalState = "EXPIRED"
	ApprovalConsumed ApprovalState = "CONSUMED"
)

type ApprovalRequest struct {
	ID          string                   `json:"id"`
	TenantID    string                   `json:"tenant_id"`
	AgentID     string                   `json:"agent_id"`
	TaskID      string                   `json:"task_id"`
	Action      agentv1.ApprovalAction   `json:"action"`
	Resource    agentv1.ApprovalResource `json:"resource"`
	PayloadHash string                   `json:"payload_hash"`
	State       ApprovalState            `json:"state"`
	ExpiresAt   time.Time                `json:"expires_at"`
	Version     int64                    `json:"version"`
	CreatedAt   time.Time                `json:"created_at"`
	UpdatedAt   time.Time                `json:"updated_at"`
}

func (r ApprovalRequest) View() agentv1.ApprovalRequestView {
	return agentv1.ApprovalRequestView{ApprovalRequestID: r.ID, TenantID: r.TenantID, AgentID: r.AgentID, TaskID: r.TaskID, Action: r.Action, Resource: r.Resource, PayloadHash: r.PayloadHash, State: string(r.State), ExpiresAt: r.ExpiresAt}
}

type ApprovalGrant struct {
	ID             string                   `json:"id"`
	RequestID      string                   `json:"request_id"`
	TenantID       string                   `json:"tenant_id"`
	AgentID        string                   `json:"agent_id"`
	TaskID         string                   `json:"task_id"`
	ApproverUserID string                   `json:"approver_user_id"`
	Action         agentv1.ApprovalAction   `json:"action"`
	Resource       agentv1.ApprovalResource `json:"resource"`
	PayloadHash    string                   `json:"payload_hash"`
	ExpiresAt      time.Time                `json:"expires_at"`
	ConsumedAt     *time.Time               `json:"consumed_at,omitempty"`
	Version        int64                    `json:"version"`
	CreatedAt      time.Time                `json:"created_at"`
}
type InvocationState string

const (
	InvocationStarted   InvocationState = "STARTED"
	InvocationCompleted InvocationState = "COMPLETED"
	InvocationFailed    InvocationState = "FAILED"
)

type Invocation struct {
	ID                  string          `json:"id"`
	TenantID            string          `json:"tenant_id"`
	AgentID             string          `json:"agent_id"`
	TaskID              string          `json:"task_id"`
	Tool                agentv1.Tool    `json:"tool"`
	IdempotencyKey      string          `json:"idempotency_key"`
	Fingerprint         string          `json:"fingerprint"`
	State               InvocationState `json:"state"`
	Response            json.RawMessage `json:"response,omitempty"`
	ExternalOperationID string          `json:"external_operation_id,omitempty"`
	Version             int64           `json:"version"`
	CreatedAt           time.Time       `json:"created_at"`
	UpdatedAt           time.Time       `json:"updated_at"`
}
type AuditRecord struct {
	ID               string                `json:"id"`
	TenantID         string                `json:"tenant_id"`
	TaskID           string                `json:"task_id"`
	AgentID          string                `json:"agent_id"`
	OnBehalfOfUserID string                `json:"on_behalf_of_user_id"`
	Tool             agentv1.Tool          `json:"tool"`
	Action           string                `json:"action,omitempty"`
	CorrelationID    string                `json:"correlation_id"`
	Outcome          string                `json:"outcome"`
	ResourceType     string                `json:"resource_type,omitempty"`
	ResourceID       string                `json:"resource_id,omitempty"`
	OperationID      string                `json:"operation_id,omitempty"`
	ErrorCode        string                `json:"error_code,omitempty"`
	Evidence         agentv1.AuditEvidence `json:"evidence,omitempty"`
	CreatedAt        time.Time             `json:"created_at"`
}

func (a AuditRecord) View() agentv1.AuditView {
	return agentv1.AuditView{AuditID: a.ID, TenantID: a.TenantID, TaskID: a.TaskID, AgentID: a.AgentID, OnBehalfOfUserID: a.OnBehalfOfUserID, Tool: a.Tool, Action: a.Action, CorrelationID: a.CorrelationID, Outcome: a.Outcome, ResourceType: a.ResourceType, ResourceID: a.ResourceID, OperationID: a.OperationID, ErrorCode: a.ErrorCode, Evidence: a.Evidence, OccurredAt: a.CreatedAt}
}

type OutboxRecord struct {
	ID          string          `json:"id"`
	TenantID    string          `json:"tenant_id"`
	Topic       string          `json:"topic"`
	AggregateID string          `json:"aggregate_id"`
	Payload     json.RawMessage `json:"payload"`
	CreatedAt   time.Time       `json:"created_at"`
}

func ValidateID(label, v string) error {
	if !agentv1.ValidID(strings.TrimSpace(v)) {
		return fmt.Errorf("invalid %s", label)
	}
	return nil
}
