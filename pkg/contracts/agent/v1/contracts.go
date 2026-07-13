// Package v1 defines stable, provider-neutral Agent Governance contracts.
package v1

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	kernelv1 "github.com/keir-research/ai-native-paas/contracts/kernel/v1"
)

const APIVersion = "agent.platform.example.com/v1"
const SemanticsVersion = "v1"

type Tool string

const (
	ToolCreateProject        Tool = "platform_create_project"
	ToolGetProject           Tool = "platform_get_project"
	ToolApplyRepositoryPatch Tool = "platform_apply_repository_patch"
	ToolCreateBranch         Tool = "platform_create_branch"
	ToolCreateMergeRequest   Tool = "platform_create_merge_request"
	ToolRequestBuild         Tool = "platform_request_build"
	ToolGetBuild             Tool = "platform_get_build"
	ToolDeploy               Tool = "platform_deploy"
	ToolGetDeployment        Tool = "platform_get_deployment"
	ToolRollback             Tool = "platform_rollback"
	ToolSetSecret            Tool = "platform_set_secret"
	ToolListSecretMetadata   Tool = "platform_list_secret_metadata"
	ToolProvisionService     Tool = "platform_provision_service"
	ToolBindService          Tool = "platform_bind_service"
	ToolAddDomain            Tool = "platform_add_domain"
	ToolGetLogs              Tool = "platform_get_logs"
	ToolGetUsage             Tool = "platform_get_usage"
	ToolRequestApproval      Tool = "platform_request_approval"
	ToolGetOperation         Tool = "platform_get_operation"
	ToolCancelOperation      Tool = "platform_cancel_operation"
)

var catalog = []Tool{ToolAddDomain, ToolApplyRepositoryPatch, ToolBindService, ToolCancelOperation, ToolCreateBranch, ToolCreateMergeRequest, ToolCreateProject, ToolDeploy, ToolGetBuild, ToolGetDeployment, ToolGetLogs, ToolGetOperation, ToolGetProject, ToolGetUsage, ToolListSecretMetadata, ToolProvisionService, ToolRequestApproval, ToolRequestBuild, ToolRollback, ToolSetSecret}

func ToolCatalog() []Tool { return append([]Tool(nil), catalog...) }
func ValidTool(v Tool) bool {
	i := sort.Search(len(catalog), func(i int) bool { return catalog[i] >= v })
	return i < len(catalog) && catalog[i] == v
}
func IsReadOnly(v Tool) bool {
	switch v {
	case ToolGetProject, ToolGetBuild, ToolGetDeployment, ToolListSecretMetadata, ToolGetLogs, ToolGetUsage, ToolGetOperation:
		return true
	}
	return false
}

var safeID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,62}$`)

func ValidID(v string) bool { return safeID.MatchString(v) }

type InvocationRequest struct {
	APIVersion       string          `json:"api_version"`
	SemanticsVersion string          `json:"semantics_version"`
	TenantID         string          `json:"tenant_id"`
	AgentID          string          `json:"agent_id"`
	TaskID           string          `json:"task_id"`
	Tool             Tool            `json:"tool"`
	Arguments        json.RawMessage `json:"arguments"`
	IdempotencyKey   string          `json:"idempotency_key"`
	CorrelationID    string          `json:"correlation_id"`
	ApprovalGrantID  string          `json:"approval_grant_id,omitempty"`
}

func (r InvocationRequest) Validate() error {
	if r.APIVersion != APIVersion || r.SemanticsVersion != SemanticsVersion {
		return errors.New("unsupported agent API semantics")
	}
	if !ValidID(r.TenantID) || !ValidID(r.AgentID) || !ValidID(r.TaskID) || !ValidTool(r.Tool) || strings.TrimSpace(r.IdempotencyKey) == "" || len(r.IdempotencyKey) > 128 || !ValidID(r.CorrelationID) {
		return errors.New("invalid invocation identity")
	}
	if len(r.Arguments) == 0 || len(r.Arguments) > 1<<20 || !json.Valid(r.Arguments) {
		return errors.New("invalid tool arguments")
	}
	if r.ApprovalGrantID != "" && !ValidID(r.ApprovalGrantID) {
		return errors.New("invalid approval grant")
	}
	return nil
}

type ResultReference struct {
	Type  string          `json:"type"`
	ID    string          `json:"id"`
	URL   string          `json:"url,omitempty"`
	State string          `json:"state,omitempty"`
	Data  json.RawMessage `json:"data,omitempty"`
}
type InvocationResponse struct {
	APIVersion   string                 `json:"api_version"`
	InvocationID string                 `json:"invocation_id"`
	Operation    *kernelv1.OperationRef `json:"operation,omitempty"`
	Result       *ResultReference       `json:"result,omitempty"`
	Error        *kernelv1.PublicError  `json:"error,omitempty"`
	Replayed     bool                   `json:"replayed"`
}

func (r InvocationResponse) Validate() error {
	if r.APIVersion != APIVersion || !ValidID(r.InvocationID) {
		return errors.New("invalid invocation response")
	}
	if (r.Operation == nil) == (r.Result == nil) {
		return errors.New("exactly one operation or result reference is required")
	}
	return nil
}

type Scope string

func ScopeForTool(t Tool) Scope { return Scope("agent.tool:" + string(t)) }

type ApprovalAction string

const (
	ApprovalDeleteProduction ApprovalAction = "application.delete.production"
	ApprovalPurgeDatabase    ApprovalAction = "service.purge.database"
	ApprovalTransferDomain   ApprovalAction = "domain.transfer"
	ApprovalIncreasePaidPlan ApprovalAction = "commercial.increase_paid_limits"
	ApprovalDeployProduction ApprovalAction = "deployment.production"
	ApprovalDisableBackups   ApprovalAction = "service.disable_backups"
	ApprovalExposeSMTP       ApprovalAction = "network.expose_smtp"
)

func ValidApprovalAction(a ApprovalAction) bool {
	switch a {
	case ApprovalDeleteProduction, ApprovalPurgeDatabase, ApprovalTransferDomain, ApprovalIncreasePaidPlan, ApprovalDeployProduction, ApprovalDisableBackups, ApprovalExposeSMTP:
		return true
	}
	return false
}

type ApprovalResource struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

func (r ApprovalResource) Validate() error {
	if !ValidID(r.Type) || !ValidID(r.ID) {
		return errors.New("invalid approval resource")
	}
	return nil
}

type ApprovalRequestView struct {
	ApprovalRequestID string           `json:"approval_request_id"`
	TenantID          string           `json:"tenant_id"`
	AgentID           string           `json:"agent_id"`
	TaskID            string           `json:"task_id"`
	Action            ApprovalAction   `json:"action"`
	Resource          ApprovalResource `json:"resource"`
	PayloadHash       string           `json:"payload_hash"`
	State             string           `json:"state"`
	ExpiresAt         time.Time        `json:"expires_at"`
}
type ApprovalGrantView struct {
	ApprovalGrantID   string           `json:"approval_grant_id"`
	ApprovalRequestID string           `json:"approval_request_id"`
	Action            ApprovalAction   `json:"action"`
	Resource          ApprovalResource `json:"resource"`
	PayloadHash       string           `json:"payload_hash"`
	ExpiresAt         time.Time        `json:"expires_at"`
}
type BudgetPolicy struct {
	MaxBuildCount   int64 `json:"max_build_count"`
	MaxBuildMinutes int64 `json:"max_build_minutes"`
	MaxDeployCount  int64 `json:"max_deploy_count"`
	RepairThreshold int64 `json:"repair_threshold"`
}

func (p BudgetPolicy) Validate() error {
	if p.MaxBuildCount < 0 || p.MaxBuildMinutes < 0 || p.MaxDeployCount < 0 || p.RepairThreshold < 1 || p.MaxBuildCount > 100000 || p.MaxBuildMinutes > 10000000 || p.MaxDeployCount > 100000 {
		return errors.New("invalid budget policy")
	}
	return nil
}

type BudgetUsage struct {
	BuildCount   int64 `json:"build_count"`
	BuildMinutes int64 `json:"build_minutes"`
	DeployCount  int64 `json:"deploy_count"`
}
type TaskView struct {
	TaskID            string       `json:"task_id"`
	TenantID          string       `json:"tenant_id"`
	AgentID           string       `json:"agent_id"`
	OnBehalfOfUserID  string       `json:"on_behalf_of_user_id"`
	CorrelationID     string       `json:"correlation_id"`
	State             string       `json:"state"`
	BudgetPolicy      BudgetPolicy `json:"budget_policy"`
	BudgetUsage       BudgetUsage  `json:"budget_usage"`
	RepairFingerprint string       `json:"repair_fingerprint,omitempty"`
	RepairCount       int64        `json:"repair_count"`
}
type AuditView struct {
	AuditID          string    `json:"audit_id"`
	TenantID         string    `json:"tenant_id"`
	TaskID           string    `json:"task_id"`
	AgentID          string    `json:"agent_id"`
	OnBehalfOfUserID string    `json:"on_behalf_of_user_id"`
	Tool             Tool      `json:"tool"`
	CorrelationID    string    `json:"correlation_id"`
	Outcome          string    `json:"outcome"`
	ResourceType     string    `json:"resource_type,omitempty"`
	ResourceID       string    `json:"resource_id,omitempty"`
	OperationID      string    `json:"operation_id,omitempty"`
	ErrorCode        string    `json:"error_code,omitempty"`
	OccurredAt       time.Time `json:"occurred_at"`
}

func DecodeStrict(raw json.RawMessage, out any) error {
	if len(raw) == 0 || len(raw) > 1<<20 {
		return errors.New("arguments size invalid")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		return err
	}
	var extra any
	if err := d.Decode(&extra); err == nil {
		return errors.New("multiple JSON values")
	}
	return nil
}
func StableFingerprint(v any) (string, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	var decoded any
	if err = json.Unmarshal(raw, &decoded); err != nil {
		return "", err
	}
	raw, err = json.Marshal(decoded)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return fmt.Sprintf("sha256:%x", sum), nil
}
func PublicError(code, message string, retryable bool, operationID string) *kernelv1.PublicError {
	message = strings.TrimSpace(message)
	if message == "" {
		message = "request failed"
	}
	return &kernelv1.PublicError{Code: code, Message: message, Retryable: retryable, OperationID: kernelv1.OperationID(operationID)}
}
