package application

import (
	"context"
	"time"

	"github.com/keir-research/ai-native-paas/internal/source/domain"
	sourcev1 "github.com/keir-research/ai-native-paas/pkg/contracts/source/v1"
)

type Clock interface{ Now() time.Time }
type IDGenerator interface{ NewID(prefix string) string }

type IdempotencyRecord struct {
	TenantID    string
	Key         string
	Command     string
	RequestHash string
	Result      []byte
	Completed   bool
	CreatedAt   time.Time
	CompletedAt time.Time
}
type OutboxRecord struct {
	ID          string
	Topic       string
	AggregateID string
	Payload     []byte
	CreatedAt   time.Time
}
type AuditRecord struct {
	ID           string
	TenantID     string
	ActorID      string
	Action       string
	ResourceType string
	ResourceID   string
	Data         []byte
	CreatedAt    time.Time
}
type WebhookReceipt struct {
	Provider   string
	EventID    string
	BodyHash   string
	ReceivedAt time.Time
}

type Tx interface {
	GetProject(id string) (domain.Project, bool)
	FindProjectBySlug(tenantID, slug string) (domain.Project, bool)
	InsertProject(domain.Project) error
	UpdateProject(domain.Project, int64) error

	GetRepository(id string) (domain.Repository, bool)
	FindRepositoryByProject(projectID string) (domain.Repository, bool)
	FindRepositoryByProviderID(provider string, providerProjectID int64) (domain.Repository, bool)
	InsertRepository(domain.Repository) error
	UpdateRepository(domain.Repository, int64) error
	ListRepositories() []domain.Repository

	GetBranch(repositoryID, name string) (domain.BranchHead, bool)
	UpsertBranch(domain.BranchHead, int64) error
	GetMergeRequest(repositoryID string, iid int64) (domain.MergeRequest, bool)
	UpsertMergeRequest(domain.MergeRequest, int64) error

	GetWorkspace(id string) (domain.Workspace, bool)
	ListWorkspaces(repositoryID string) []domain.Workspace
	InsertWorkspace(domain.Workspace) error
	UpdateWorkspace(domain.Workspace, int64) error

	GetIdempotency(tenantID, key string) (IdempotencyRecord, bool)
	PutIdempotency(IdempotencyRecord) error
	ReceiveWebhook(WebhookReceipt) (bool, error)
	AppendOutbox(OutboxRecord) error
	AppendAudit(AuditRecord) error
}

type Store interface {
	Transact(context.Context, func(Tx) error) error
}

type ProviderRepository struct {
	ID                int64
	NamespaceID       int64
	Path              string
	PathWithNamespace string
	WebURL            string
	DefaultBranch     string
	Description       string
	Archived          bool
}
type ProviderCredential struct {
	ID        string
	Username  string
	Token     string
	ExpiresAt time.Time
}
type CreateRepositoryRequest struct {
	NamespaceID   int64
	Name          string
	Path          string
	DefaultBranch string
	CorrelationID string
}
type CreateMergeRequestRequest struct {
	ProjectID                                      int64
	SourceBranch, TargetBranch, Title, Description string
}
type ProviderMergeRequest struct {
	IID                                                             int64
	State, SourceBranch, TargetBranch, HeadSHA, WebURL, Description string
}
type ProviderMergeRequestNote struct {
	ID   int64
	Body string
}

type BootstrapFile struct {
	Path       string
	Content    []byte
	Executable bool
	Update     bool
}

type BootstrapRepositoryRequest struct {
	ProviderProjectID int64
	Branch            string
	ExpectedBaseSHA   string
	CommitMessage     string
	Files             []BootstrapFile
}

type RepositoryBootstrapper interface {
	BootstrapRepository(context.Context, BootstrapRepositoryRequest) (string, error)
}

type GitProvider interface {
	CreateRepository(context.Context, CreateRepositoryRequest) (ProviderRepository, error)
	FindRepositoryByCorrelation(context.Context, int64, string) (ProviderRepository, bool, error)
	GetRepository(context.Context, int64) (ProviderRepository, error)
	ProtectBranch(context.Context, int64, string) error
	GetBranchHead(context.Context, int64, string) (string, error)
	CreateCredential(context.Context, int64, string, time.Time) (ProviderCredential, error)
	RevokeCredential(context.Context, int64, string) error
	CreateMergeRequest(context.Context, CreateMergeRequestRequest) (ProviderMergeRequest, error)
	FindOpenMergeRequest(context.Context, int64, string, string) (ProviderMergeRequest, bool, error)
	CreateMergeRequestNote(context.Context, int64, int64, string) (ProviderMergeRequestNote, error)
	FindMergeRequestNoteByMarker(context.Context, int64, int64, string) (ProviderMergeRequestNote, bool, error)
	ArchiveRepository(context.Context, int64) (ProviderRepository, error)
	UnarchiveRepository(context.Context, int64) (ProviderRepository, error)
	DeleteRepository(context.Context, int64) error
}

type ProjectPurgeAuthorization struct {
	TenantID          string
	ProjectID         string
	RepositoryID      string
	ProviderProjectID int64
	ActorID           string
	ApprovalGrantID   string
	IdempotencyKey    string
	Now               time.Time
}

// ProjectPurgeAuthorizer must consume a grant once while treating a retry with
// the same idempotency key as the same authorized command.
type ProjectPurgeAuthorizer interface {
	VerifyAndConsumeProjectPurge(context.Context, ProjectPurgeAuthorization) error
}

type PushEvent struct {
	EventID           string
	Provider          string
	ProviderProjectID int64
	Branch            string
	BeforeSHA         string
	AfterSHA          string
	OccurredAt        time.Time
	BodyHash          string
}
type MergeRequestEvent struct {
	EventID                                            string
	Provider                                           string
	ProviderProjectID                                  int64
	IID                                                int64
	Action, State, SourceBranch, TargetBranch, HeadSHA string
	OccurredAt                                         time.Time
	BodyHash                                           string
}
type NormalizedWebhook struct {
	Push         *PushEvent
	MergeRequest *MergeRequestEvent
	SourceEvent  sourcev1.SourceEvent
}

type WebhookVerifier interface {
	Verify(headers map[string][]string, rawBody []byte, now time.Time) (string, error)
}
type WebhookNormalizer interface {
	Normalize(eventID string, rawBody []byte, now time.Time) (NormalizedWebhook, error)
}

type PatchOperation struct {
	Path       string
	Content    []byte
	Delete     bool
	Executable bool
}
type WorkspaceGit interface {
	CloneExact(context.Context, string, string, string, ProviderCredential) (string, error)
	Apply(context.Context, string, []PatchOperation) error
	Commit(context.Context, string, string, string, string) (string, error)
	Push(context.Context, string, string, string, string, ProviderCredential) (bool, error)
	RemoteHead(context.Context, string, string, ProviderCredential) (string, error)
	Cleanup(context.Context, string) error
}
