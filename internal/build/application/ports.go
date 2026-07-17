package application

import (
	"context"
	"io"
	"time"

	"github.com/keir-research/ai-native-paas/internal/build/domain"
	buildv1 "github.com/keir-research/ai-native-paas/pkg/contracts/build/v1"
	buildv2 "github.com/keir-research/ai-native-paas/pkg/contracts/build/v2"
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
	ID, Topic, AggregateID string
	Payload                []byte
	CreatedAt              time.Time
}
type AuditRecord struct {
	ID, TenantID, ActorID, Action, ResourceType, ResourceID string
	Data                                                    []byte
	CreatedAt                                               time.Time
}

type Tx interface {
	GetBuild(id string) (domain.Build, bool)
	FindBuildByIdentity(tenantID, identity string) (domain.Build, bool)
	InsertBuild(domain.Build) error
	UpdateBuild(domain.Build, int64) error
	ListBuildsByProject(tenantID, projectID string) []domain.Build

	GetArtifact(id string) (domain.Artifact, bool)
	FindArtifactByBuild(buildID string) (domain.Artifact, bool)
	InsertArtifact(domain.Artifact) error
	UpdateArtifact(domain.Artifact, int64) error

	GetIdempotency(tenantID, key string) (IdempotencyRecord, bool)
	PutIdempotency(IdempotencyRecord) error
	AppendOutbox(OutboxRecord) error
	AppendAudit(AuditRecord) error
}
type Store interface {
	Transact(context.Context, func(Tx) error) error
}

type SourceLimits struct {
	MaxBytes        int64
	MaxFiles        int
	AllowSubmodules bool
}
type SourceSnapshot struct {
	Revision  sourcev1.SourceRevision
	Path      string
	SizeBytes int64
	FileCount int
	Cleanup   func() error
}
type SourceFetcher interface {
	Fetch(context.Context, string, sourcev1.SourceRevision, SourceLimits) (SourceSnapshot, error)
}

type BackendKind string

const (
	BackendBuildpacks BackendKind = "buildpacks"
	BackendDockerfile BackendKind = "dockerfile-vm"
)

type Detection struct {
	Runtime     string
	Backend     BackendKind
	BuildpackID string
	Entrypoint  []string
	Evidence    []string
}
type Detector interface {
	Detect(context.Context, string, domain.BuildConfig) (Detection, error)
}

type BuildPhase string

const (
	PhaseFetch  BuildPhase = "fetch"
	PhaseDetect BuildPhase = "detect"
	PhaseBuild  BuildPhase = "build"
	PhaseExport BuildPhase = "export"
)

type BuildSecret struct {
	Name          string
	Value         string
	AllowedPhases []BuildPhase
}
type BuildSecretProvider interface {
	ResolveBuildSecrets(context.Context, string, []string) ([]BuildSecret, error)
}
type BuildOutput struct {
	OCILayoutPath  string
	ManifestDigest string
	MediaType      string
	Metadata       map[string]string
}
type BuildExecutionRequest struct {
	BuildID        string
	TenantID       string
	BuilderDigest  string
	RunImageDigest string
	Source         SourceSnapshot
	Detection      Detection
	Config         domain.BuildConfig
	BuildSpec      *buildv2.BuildSpec
	Environment    map[string]string
	Secrets        []BuildSecret
	LogWriter      io.Writer
	Repository     string
}
type Builder interface {
	Build(context.Context, BuildExecutionRequest) (BuildOutput, error)
	Cancel(context.Context, string) error
}

type IsolationBoundary string

const IsolationDisposableWorkspaceVM IsolationBoundary = "disposable-workspace-vm"

// IsolatedBuilder is a fail-closed capability declaration. Merely registering
// a Dockerfile builder is insufficient: the adapter must prove that untrusted
// instructions execute across the disposable workspace boundary.
type IsolatedBuilder interface {
	Builder
	IsolationBoundary() IsolationBoundary
}

type PublishedArtifact struct {
	Repository string `json:"repository"`
	Digest     string `json:"digest"`
	MediaType  string `json:"media_type"`
}
type Registry interface {
	Publish(context.Context, string, string, BuildOutput) (PublishedArtifact, error)
	Resolve(context.Context, string, string) (PublishedArtifact, error)
	// StoreAttachment persists a trust document as an OCI referrer of an
	// immutable subject.  The subject is deliberately part of this port: a
	// digest-only document stored "somewhere in the repository" cannot prove
	// that its SBOM, signature or provenance belongs to the artifact we are
	// about to release.
	StoreAttachment(context.Context, string, PublishedArtifact, string, []byte) (string, error)
}
type SBOMResult struct {
	Digest    string
	MediaType string
	Document  []byte
}
type SBOMGenerator interface {
	Generate(context.Context, SourceSnapshot, PublishedArtifact) (SBOMResult, error)
}
type Scanner interface {
	Scan(context.Context, buildv1.ArtifactRef, SBOMResult) (domain.ScanResult, error)
}
type Signer interface {
	Sign(context.Context, string, string) (domain.SignatureRecord, error)
}
type SignatureVerifier interface {
	Verify(context.Context, string, domain.SignatureRecord) error
}
type ProvenanceMaterials struct {
	BuildID, Repository, SourceSHA, BuildSpecDigest, BuilderDigest, OutputDigest string
	StartedAt, FinishedAt                                                        time.Time
}
type ProvenanceResult struct {
	Digest    string
	MediaType string
	Document  []byte
}
type ProvenanceAttestor interface {
	Attest(context.Context, ProvenanceMaterials) (ProvenanceResult, error)
}
type ProvenanceVerifier interface {
	Verify(context.Context, []byte) (ProvenanceResult, error)
}
type LogStore interface {
	Writer(buildID string, secrets []string) io.Writer
	Read(context.Context, string) ([]byte, error)
}
