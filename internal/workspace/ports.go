package workspace

import (
	"context"
	"time"

	infrastructurev1 "github.com/keir-research/ai-native-paas/pkg/contracts/infrastructure/v1"
	sourcev2 "github.com/keir-research/ai-native-paas/pkg/contracts/source/v2"
	workspacev1 "github.com/keir-research/ai-native-paas/pkg/contracts/workspace/v1"
)

type Clock interface{ Now() time.Time }
type IDGenerator interface{ New(string) string }

type CommandBudgetRequest struct {
	TenantID, ProjectID, TaskID, WorkspaceID, CommandID string
	RequestedSeconds                                    int64
	RequestedAt                                         time.Time
}

type CommandBudgetLease struct {
	ReservationID  string
	GrantedSeconds int64
	NotAfter       time.Time
}

type CommandBudgetGateway interface {
	ReserveAndCommit(context.Context, CommandBudgetRequest) (CommandBudgetLease, error)
}

type Store interface {
	CreateWorkspace(context.Context, Workspace) (Workspace, bool, error)
	GetWorkspace(context.Context, string, string, string) (Workspace, error)
	UpdateWorkspace(context.Context, Workspace, int64) error
	ClaimWorkspace(context.Context, string, string, time.Time, time.Time) (Workspace, error)
	ListExpired(context.Context, time.Time, int) ([]Workspace, error)

	CreateCommand(context.Context, Command) (Command, bool, error)
	GetCommand(context.Context, string, string, string) (Command, error)
	UpdateCommand(context.Context, Command, int64) error
	ListTimedOut(context.Context, time.Time, int) ([]Command, error)
	AcquireSerialization(context.Context, string, string, string) (bool, error)
	ReleaseSerialization(context.Context, string, string, string) error
	PutCommitReceipt(context.Context, CommitReceiptScope, sourcev2.AgentCommitReceipt) error
	GetCommitReceipt(context.Context, string, string, string) (sourcev2.AgentCommitReceipt, error)
	CreateSourceChangePlan(context.Context, SourceChangePlan) (SourceChangePlan, error)
	GetSourceChangePlan(context.Context, string, string, string) (SourceChangePlan, error)
	CreateSourceApproval(context.Context, SourceApprovalGrant) (SourceApprovalGrant, error)
	GetActiveSourceApproval(context.Context, string, string, string, string, string, time.Time) (SourceApprovalGrant, error)
	AuthorizeSourceCommit(context.Context, SourceCommitAuthorization) (SourceChangePlan, error)
}

type NetworkIsolation struct {
	VPCID                  string
	PrivateAddressOnly     bool
	DenyAllInbound         bool
	OutboundGatewayMTLS    bool
	AllowedEgressHosts     []string
	EgressGatewayCIDRs     []string
	DNSResolverCIDRs       []string
	DeniedCIDRs            []string
	DeniedDestinationPorts []int
}

type ProviderCreateRequest struct {
	WorkspaceID      string
	TenantID         string
	ProjectID        string
	TaskID           string
	AgentID          string
	CorrelationID    string
	ImageDigest      string
	CPUMillis        int64
	MemoryMiB        int64
	ExpiresAt        time.Time
	NetworkProfile   string
	NetworkIsolation NetworkIsolation
}

type ProviderVM struct {
	VMID               string
	DiskIDs            []string
	FirewallGroupIDs   []string
	CorrelationID      string
	ImageDigest        string
	NetworkProfile     string
	PrivateAddressOnly bool
	DenyAllInbound     bool
	OutboundAgentReady bool
}

type DestroyEvidence struct {
	VMAbsent               bool
	AbsentDiskIDs          []string
	AbsentFirewallGroupIDs []string
}

type Provider interface {
	FindByCorrelation(context.Context, string) (ProviderVM, error)
	Create(context.Context, ProviderCreateRequest) (ProviderVM, error)
	Destroy(context.Context, ProviderVM) (DestroyEvidence, error)
}

type CommandEnvelope struct {
	CommandID        string
	WorkspaceID      string
	ProjectID        string
	TaskID           string
	Spec             workspacev1.CommandSpec
	CredentialLeases []string
	BudgetLease      CommandBudgetLease
}

type DispatchReceipt struct {
	CommandID      string
	WorkspaceID    string
	VMID           string
	AgentSessionID string
	Accepted       bool
}

// AgentSessions is implemented by an outbound mTLS session registry. The
// control plane never dials a management port on the VM and never starts a
// local process.
type AgentSessions interface {
	Connected(context.Context, string, string) (bool, error)
	Dispatch(context.Context, CommandEnvelope) (DispatchReceipt, error)
	RequestCancel(context.Context, string, string) error
	Close(context.Context, string) error
}

type LeaseRevoker interface {
	Revoke(context.Context, string, string) error
}

type CredentialSourceRequest struct {
	TenantID         string
	ProjectID        string
	WorkspaceID      string
	TaskID           string
	CommandID        string
	AgentSessionID   string
	VMID             string
	EnvironmentRefs  map[string]string
	CredentialLeases []string
}

type CredentialSource interface {
	Resolve(context.Context, CredentialSourceRequest) (workspacev1.AgentCredentialView, error)
}

type CommandOutputScope struct {
	TenantID       string
	ProjectID      string
	WorkspaceID    string
	TaskID         string
	CommandID      string
	AgentSessionID string
	VMID           string
}

type CommandOutputStore interface {
	PutChunk(context.Context, CommandOutputScope, workspacev1.AgentOutputChunk) error
}

type PlanReceiptScope struct {
	TenantID    string
	ProjectID   string
	WorkspaceID string
	TaskID      string
	CommandID   string
	ActorID     string
}

type PlanReceiptStore interface {
	PutPlanReceipt(context.Context, PlanReceiptScope, infrastructurev1.AgentPlanReceipt) error
}

type CommitReceiptScope struct {
	TenantID    string
	ProjectID   string
	WorkspaceID string
	TaskID      string
	CommandID   string
	ActorID     string
}
