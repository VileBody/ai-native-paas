package domain

import (
	"strings"
	"time"

	buildv1 "github.com/keir-research/ai-native-paas/pkg/contracts/build/v1"
	sourcev1 "github.com/keir-research/ai-native-paas/pkg/contracts/source/v1"
)

type ExecutionBackend string

const (
	ExecutionBuildpacks ExecutionBackend = "buildpacks"
	ExecutionDockerfile ExecutionBackend = "dockerfile-vm"
)

type Build struct {
	ID                    string
	TenantID              string
	Identity              string
	Source                sourcev1.SourceRevision
	Config                BuildConfig
	BuilderDigest         string
	RunImageDigest        string
	PlatformBuildVersion  string
	Runtime               string
	Backend               ExecutionBackend
	BuildpackID           string
	State                 buildv1.BuildState
	CorrelationID         string
	OriginalCorrelationID string
	ArtifactID            string
	FailureCode           string
	FailureMessage        string
	Retryable             bool
	Attempt               int
	Version               int64
	CreatedAt             time.Time
	UpdatedAt             time.Time
	StartedAt             time.Time
	CompletedAt           time.Time
}

func NewBuild(id, tenantID, identity, correlationID string, source sourcev1.SourceRevision, config BuildConfig, builderDigest, runImageDigest, platformVersion string, now time.Time) (Build, error) {
	if strings.TrimSpace(id) == "" || strings.TrimSpace(tenantID) == "" || strings.TrimSpace(identity) == "" || strings.TrimSpace(correlationID) == "" {
		return Build{}, NewError(CodeInvalidArgument, "build identity fields are required")
	}
	var err error
	source, config, err = NormalizeBuildInput(source, config)
	if err != nil {
		return Build{}, err
	}
	return Build{
		ID: id, TenantID: tenantID, Identity: identity, Source: source, Config: config,
		BuilderDigest: builderDigest, RunImageDigest: runImageDigest, PlatformBuildVersion: platformVersion,
		State: buildv1.BuildQueued, CorrelationID: correlationID, OriginalCorrelationID: correlationID,
		Attempt: 1, Version: 1, CreatedAt: now.UTC(), UpdatedAt: now.UTC(),
	}, nil
}

func (b Build) Terminal() bool {
	switch b.State {
	case buildv1.BuildSucceeded, buildv1.BuildFailedUserCode, buildv1.BuildFailedPlatform,
		buildv1.BuildCanceled, buildv1.BuildSuperseded, buildv1.BuildTimedOut:
		return true
	default:
		return false
	}
}

var allowedBuildTransitions = map[buildv1.BuildState]map[buildv1.BuildState]bool{
	buildv1.BuildQueued: {
		buildv1.BuildFetchingSource: true, buildv1.BuildCanceled: true, buildv1.BuildSuperseded: true,
	},
	buildv1.BuildFetchingSource: {
		buildv1.BuildDetecting: true, buildv1.BuildFailedUserCode: true, buildv1.BuildFailedPlatform: true,
		buildv1.BuildCanceled: true, buildv1.BuildTimedOut: true, buildv1.BuildSuperseded: true,
	},
	buildv1.BuildDetecting: {
		buildv1.BuildBuilding: true, buildv1.BuildFailedUserCode: true, buildv1.BuildFailedPlatform: true,
		buildv1.BuildCanceled: true, buildv1.BuildTimedOut: true, buildv1.BuildSuperseded: true,
	},
	buildv1.BuildBuilding: {
		buildv1.BuildExporting: true, buildv1.BuildFailedUserCode: true, buildv1.BuildFailedPlatform: true,
		buildv1.BuildCanceled: true, buildv1.BuildTimedOut: true, buildv1.BuildSuperseded: true,
	},
	buildv1.BuildExporting: {
		buildv1.BuildScanning: true, buildv1.BuildFailedUserCode: true, buildv1.BuildFailedPlatform: true,
		buildv1.BuildCanceled: true, buildv1.BuildTimedOut: true, buildv1.BuildSuperseded: true,
	},
	buildv1.BuildScanning: {
		buildv1.BuildSigning: true, buildv1.BuildFailedUserCode: true, buildv1.BuildFailedPlatform: true,
		buildv1.BuildCanceled: true, buildv1.BuildTimedOut: true,
	},
	buildv1.BuildSigning: {
		buildv1.BuildSucceeded: true, buildv1.BuildFailedUserCode: true, buildv1.BuildFailedPlatform: true,
		buildv1.BuildCanceled: true, buildv1.BuildTimedOut: true,
	},
}

func (b *Build) Transition(to buildv1.BuildState, now time.Time) error {
	if b.Terminal() {
		if b.State == to {
			return nil
		}
		return NewError(CodeConflict, "terminal build state is immutable")
	}
	if !allowedBuildTransitions[b.State][to] {
		return NewError(CodeConflict, "invalid build transition")
	}
	b.State = to
	b.Version++
	b.UpdatedAt = now.UTC()
	if b.StartedAt.IsZero() && to == buildv1.BuildFetchingSource {
		b.StartedAt = now.UTC()
	}
	if b.Terminal() {
		b.CompletedAt = now.UTC()
	}
	return nil
}

func (b *Build) SelectExecution(runtime string, backend ExecutionBackend, buildpackID string, now time.Time) error {
	if b.State != buildv1.BuildDetecting {
		return NewError(CodeConflict, "execution backend can only be selected while detecting")
	}
	runtime = strings.TrimSpace(strings.ToLower(runtime))
	buildpackID = strings.TrimSpace(buildpackID)
	if runtime == "" {
		return NewError(CodeInvalidArgument, "runtime is required")
	}
	switch backend {
	case ExecutionBuildpacks:
		if buildpackID == "" {
			return NewError(CodeInvalidArgument, "buildpack id is required")
		}
	case ExecutionDockerfile:
		if buildpackID != "" {
			return NewError(CodeInvalidArgument, "dockerfile backend cannot have a buildpack id")
		}
	default:
		return NewError(CodeInvalidArgument, "unsupported execution backend")
	}
	b.Runtime = runtime
	b.Backend = backend
	b.BuildpackID = buildpackID
	return b.Transition(buildv1.BuildBuilding, now)
}

func (b *Build) Cancel(now time.Time) error {
	if b.State == buildv1.BuildCanceled {
		return nil
	}
	return b.Transition(buildv1.BuildCanceled, now)
}
func (b *Build) Supersede(now time.Time, allowRunning bool) error {
	if b.State == buildv1.BuildSuperseded {
		return nil
	}
	if b.State != buildv1.BuildQueued && !allowRunning {
		return NewError(CodeConflict, "running build cannot be superseded by policy")
	}
	return b.Transition(buildv1.BuildSuperseded, now)
}
func (b *Build) Fail(state buildv1.BuildState, code, message string, retryable bool, now time.Time) error {
	if state != buildv1.BuildFailedUserCode && state != buildv1.BuildFailedPlatform && state != buildv1.BuildTimedOut {
		return NewError(CodeInvalidArgument, "invalid failure state")
	}
	if err := b.Transition(state, now); err != nil {
		return err
	}
	b.FailureCode = truncate(code, 128)
	b.FailureMessage = truncate(message, 2048)
	b.Retryable = retryable
	return nil
}
func (b *Build) Succeed(artifactID string, now time.Time) error {
	if strings.TrimSpace(artifactID) == "" {
		return NewError(CodeInvalidArgument, "artifact id required")
	}
	if err := b.Transition(buildv1.BuildSucceeded, now); err != nil {
		return err
	}
	b.ArtifactID = artifactID
	return nil
}
func (b Build) Retry(newID, correlationID string, now time.Time) (Build, error) {
	if !b.Terminal() || b.State == buildv1.BuildSucceeded {
		return Build{}, NewError(CodeConflict, "only failed terminal build can be retried")
	}
	retry, err := NewBuild(newID, b.TenantID, b.Identity, correlationID, b.Source, b.Config, b.BuilderDigest, b.RunImageDigest, b.PlatformBuildVersion, now)
	if err != nil {
		return Build{}, err
	}
	retry.OriginalCorrelationID = b.OriginalCorrelationID
	retry.Attempt = b.Attempt + 1
	return retry, nil
}

func truncate(v string, limit int) string {
	if len(v) <= limit {
		return v
	}
	return v[:limit]
}
