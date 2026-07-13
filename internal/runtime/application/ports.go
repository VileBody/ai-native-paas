package application

import (
	"context"
	"time"

	"github.com/keir-research/ai-native-paas/internal/runtime/domain"
	runtimev1 "github.com/keir-research/ai-native-paas/pkg/contracts/runtime/v1"
)

type Store interface {
	Transact(context.Context, func(Tx) error) error
}

type Tx interface {
	GetApplication(string) (domain.Application, bool)
	FindApplicationByTenantName(string, string) (domain.Application, bool)
	InsertApplication(domain.Application) error
	UpdateApplication(domain.Application, int64) error

	GetEnvironment(string) (domain.Environment, bool)
	FindEnvironmentByApplicationName(string, string) (domain.Environment, bool)
	ListEnvironmentsByApplication(string) []domain.Environment
	InsertEnvironment(domain.Environment) error
	UpdateEnvironment(domain.Environment, int64) error

	GetRuntimeCell(string) (domain.RuntimeCell, bool)
	ListRuntimeCells() []domain.RuntimeCell
	InsertRuntimeCell(domain.RuntimeCell) error
	UpdateRuntimeCell(domain.RuntimeCell, int64) error

	GetPlacement(string) (domain.Placement, bool)
	GetCurrentPlacement(string) (domain.Placement, bool)
	InsertPlacement(domain.Placement) error
	UpdatePlacement(domain.Placement, int64) error
	InsertPlacementMigration(domain.PlacementMigration) error

	GetRelease(string) (domain.Release, bool)
	FindReleaseByIdentity(string, string, string) (domain.Release, bool)
	ListReleasesByEnvironment(string) []domain.Release
	InsertRelease(domain.Release) error
	UpdateRelease(domain.Release, int64) error

	GetDeployment(string) (domain.Deployment, bool)
	FindDeploymentByRelease(string) (domain.Deployment, bool)
	ListDeploymentsByEnvironment(string) []domain.Deployment
	InsertDeployment(domain.Deployment) error
	UpdateDeployment(domain.Deployment, int64) error

	GetGitOpsCommitByRelease(string) (domain.GitOpsCommitRecord, bool)
	InsertGitOpsCommit(domain.GitOpsCommitRecord) error
	InsertQuarantine(domain.QuarantineRecord) error

	GetIdempotency(string, string, string) (domain.IdempotencyRecord, bool)
	InsertIdempotency(domain.IdempotencyRecord) error
	AppendOutbox(domain.OutboxRecord) error
	AppendAudit(domain.AuditRecord) error
}

type Clock interface{ Now() time.Time }
type IDGenerator interface{ New(prefix string) string }
type UnitCatalog interface{ Units(name string) (int, bool) }

type StaticUnitCatalog map[string]int

func (c StaticUnitCatalog) Units(name string) (int, bool) { value, ok := c[name]; return value, ok }

type PlacementScheduler interface {
	Select([]domain.RuntimeCell, string, runtimev1.IsolationClass, int) (domain.RuntimeCell, error)
}

type Renderer interface {
	Render(RenderInput) (RenderedBundle, error)
}
type RenderInput struct {
	Application domain.Application
	Environment domain.Environment
	Release     domain.Release
	Placement   domain.Placement
	Cell        domain.RuntimeCell
}
type RenderedBundle struct {
	CellID, ReleaseID, Path, ManifestHash string
	Files                                 map[string][]byte
}

type GitOpsRepository interface {
	Commit(context.Context, CommitRequest) (CommitResult, error)
	FindByRelease(context.Context, string, string) (CommitResult, bool, error)
}
type CommitRequest struct {
	Bundle       RenderedBundle
	DeploymentID string
	ActorID      string
}
type CommitResult struct {
	CommitSHA, Path, ManifestHash string
}

type RuntimeObjectRef struct{ CellID, Namespace, Name string }
type RuntimeObserver interface {
	GetPaaSApp(context.Context, RuntimeObjectRef) (runtimev1.PaaSApp, bool, error)
}

type CreateApplicationRequest struct {
	TenantID, ProjectID, Name, ActorID, IdempotencyKey string
}
type CreateEnvironmentRequest struct {
	TenantID, ApplicationID, Name, ActorID, IdempotencyKey string
	Default                                                bool
}
type RegisterCellRequest struct {
	ID, Region                                                  string
	Isolation                                                   []runtimev1.IsolationClass
	CapacityUnits                                               int
	GitOpsRepository, ClusterServer, ArgoProject, IngressDomain string
}
type RollbackRequest struct {
	TenantID, EnvironmentID, TargetReleaseID, ActorID, IdempotencyKey string
	CriticalOverride                                                  bool
}
type ExplicitMigrationRequest struct {
	TenantID, EnvironmentID, TargetCellID, ActorID string
}
