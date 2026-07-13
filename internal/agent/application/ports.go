package application

import (
	"context"
	"encoding/json"
	kernelv1 "github.com/keir-research/ai-native-paas/contracts/kernel/v1"
	"github.com/keir-research/ai-native-paas/internal/agent/domain"
	attachmentsv1 "github.com/keir-research/ai-native-paas/pkg/contracts/attachments/v1"
	buildv1 "github.com/keir-research/ai-native-paas/pkg/contracts/build/v1"
	commercev1 "github.com/keir-research/ai-native-paas/pkg/contracts/commerce/v1"
	runtimev1 "github.com/keir-research/ai-native-paas/pkg/contracts/runtime/v1"
	sourcev1 "github.com/keir-research/ai-native-paas/pkg/contracts/source/v1"
	"time"
)

type Clock interface{ Now() time.Time }
type IDGenerator interface{ NewID(string) string }

type Tx interface {
	GetPrincipal(string) (domain.AgentPrincipal, bool)
	InsertPrincipal(domain.AgentPrincipal) error
	UpdatePrincipal(domain.AgentPrincipal, int64) error
	GetTask(string) (domain.AgentTask, bool)
	InsertTask(domain.AgentTask) error
	UpdateTask(domain.AgentTask, int64) error
	FindInvocation(string, string, string) (domain.Invocation, bool)
	GetInvocation(string) (domain.Invocation, bool)
	InsertInvocation(domain.Invocation) error
	UpdateInvocation(domain.Invocation, int64) error
	GetApprovalRequest(string) (domain.ApprovalRequest, bool)
	InsertApprovalRequest(domain.ApprovalRequest) error
	UpdateApprovalRequest(domain.ApprovalRequest, int64) error
	GetApprovalGrant(string) (domain.ApprovalGrant, bool)
	InsertApprovalGrant(domain.ApprovalGrant) error
	UpdateApprovalGrant(domain.ApprovalGrant, int64) error
	AppendAudit(domain.AuditRecord) error
	ListAudit(string, string) []domain.AuditRecord
	AppendOutbox(domain.OutboxRecord) error
}
type Store interface {
	Transact(context.Context, func(Tx) error) error
}

type ProjectRef struct {
	ProjectID    string `json:"project_id"`
	RepositoryID string `json:"repository_id"`
	WebURL       string `json:"web_url,omitempty"`
	State        string `json:"state"`
}
type CommitRef struct {
	ProjectID    string `json:"project_id"`
	RepositoryID string `json:"repository_id"`
	Branch       string `json:"branch"`
	CommitSHA    string `json:"commit_sha"`
}
type MergeRequestRef struct {
	ProjectID      string `json:"project_id"`
	MergeRequestID string `json:"merge_request_id"`
	URL            string `json:"url,omitempty"`
	State          string `json:"state"`
}
type SourceGateway interface {
	CreateProject(context.Context, string, string, string, string) (ProjectRef, error)
	GetProject(context.Context, string, string) (ProjectRef, error)
	ApplyPatch(context.Context, string, string, ApplyPatchArguments, string) (CommitRef, error)
	CreateBranch(context.Context, string, string, CreateBranchArguments, string) (CommitRef, error)
	CreateMergeRequest(context.Context, string, string, CreateMergeRequestArguments, string) (MergeRequestRef, error)
	Reconcile(context.Context, string, string) error
}
type BuildResult struct {
	Build     buildv1.BuildView
	Operation *kernelv1.OperationRef
}
type BuildGateway interface {
	Request(context.Context, string, sourcev1.SourceRevision, int64, string, string) (BuildResult, error)
	Get(context.Context, string, string) (BuildResult, error)
	Resume(context.Context, string, string) (BuildResult, error)
}
type RuntimeResult struct {
	Deployment runtimev1.DeploymentRef
	Status     *runtimev1.RuntimeStatus
	Operation  *kernelv1.OperationRef
}
type RuntimeGateway interface {
	Deploy(context.Context, runtimev1.DeployRequest, int64) (RuntimeResult, error)
	Get(context.Context, string, string) (RuntimeResult, error)
	Rollback(context.Context, string, string, string, int64, string) (RuntimeResult, error)
	Reconcile(context.Context, string, string) error
}
type AttachmentGateway interface{ attachmentsv1.Service }
type EntitlementGateway interface {
	Check(context.Context, commercev1.EntitlementRequest) (commercev1.EntitlementDecision, error)
}
type OperationGateway interface {
	Get(context.Context, string, string) (kernelv1.OperationSnapshot, error)
	Cancel(context.Context, string, string) error
}
type LogGateway interface {
	GetLogs(context.Context, string, string, int) ([]string, error)
}
type UsageGateway interface {
	GetUsage(context.Context, string, string) (commercev1.InvoicePreview, error)
}

type ProviderError struct {
	Message     string
	Retryable   bool
	OperationID string
	Cause       error
}

func (e *ProviderError) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}
func (e *ProviderError) Unwrap() error { return e.Cause }

type Service struct {
	Store       Store
	Clock       Clock
	IDs         IDGenerator
	Source      SourceGateway
	Builds      BuildGateway
	Runtime     RuntimeGateway
	Attachments AttachmentGateway
	Commerce    EntitlementGateway
	Operations  OperationGateway
	Logs        LogGateway
	Usage       UsageGateway
}

func (s *Service) require() error {
	if s == nil || s.Store == nil || s.Clock == nil || s.IDs == nil {
		return domain.NewError(domain.CodeUnavailable, "agent governance dependencies unavailable")
	}
	return nil
}
func (s *Service) now() time.Time             { return s.Clock.Now().UTC() }
func (s *Service) newID(prefix string) string { return s.IDs.NewID(prefix) }
func raw(v any) json.RawMessage               { b, _ := json.Marshal(v); return b }
