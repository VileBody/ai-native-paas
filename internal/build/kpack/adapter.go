package kpack

import (
	"context"
	"time"

	"github.com/keir-research/ai-native-paas/internal/build/application"
	"github.com/keir-research/ai-native-paas/internal/build/domain"
	buildv1 "github.com/keir-research/ai-native-paas/pkg/contracts/build/v1"
	sourcev1 "github.com/keir-research/ai-native-paas/pkg/contracts/source/v1"
)

type BuildSpec struct {
	BuildID, TenantID, Destination, BuildpackID, BuilderDigest, RunImageDigest string
	Source                                                                     sourcev1.SourceRevision
	Labels                                                                     map[string]string
}
type Status struct {
	Phase, ImageDigest, MediaType, FailureReason, FailureMessage string
	Owned                                                        bool
}
type Client interface {
	Create(context.Context, BuildSpec) error
	Get(context.Context, string) (Status, error)
	Delete(context.Context, string) error
}
type Adapter struct {
	Client       Client
	PollInterval time.Duration
}

func (a Adapter) Build(ctx context.Context, request application.BuildExecutionRequest) (application.BuildOutput, error) {
	if a.Client == nil {
		return application.BuildOutput{}, domain.NewError(domain.CodeUnavailable, "kpack client unavailable")
	}
	spec := BuildSpec{BuildID: request.BuildID, TenantID: request.TenantID, Destination: request.Repository, BuildpackID: request.Detection.BuildpackID, BuilderDigest: request.BuilderDigest, RunImageDigest: request.RunImageDigest, Source: request.Source.Revision, Labels: map[string]string{"platform.example.com/owner": "build-domain", "platform.example.com/build-id": request.BuildID}}
	if err := a.Client.Create(ctx, spec); err != nil {
		return application.BuildOutput{}, domain.Wrap(domain.CodePlatformFailure, "create kpack build", err)
	}
	interval := a.PollInterval
	if interval <= 0 {
		interval = 10 * time.Millisecond
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		status, err := a.Client.Get(ctx, request.BuildID)
		if err != nil {
			return application.BuildOutput{}, domain.Retryable(domain.CodePlatformFailure, "read kpack status", err)
		}
		switch status.Phase {
		case "SUCCEEDED":
			if !buildv1.ValidDigest(status.ImageDigest) {
				return application.BuildOutput{}, domain.NewError(domain.CodePlatformFailure, "kpack succeeded without immutable image digest")
			}
			return application.BuildOutput{ManifestDigest: status.ImageDigest, MediaType: status.MediaType, Metadata: map[string]string{"backend": "kpack"}}, nil
		case "FAILED_USER_CODE":
			return application.BuildOutput{}, domain.NewError(domain.CodeUserFailure, status.FailureReason+": "+status.FailureMessage)
		case "FAILED_PLATFORM":
			return application.BuildOutput{}, domain.Retryable(domain.CodePlatformFailure, status.FailureReason, statusError(status))
		case "CANCELED":
			return application.BuildOutput{}, domain.NewError(domain.CodeConflict, "kpack build canceled")
		}
		select {
		case <-ctx.Done():
			return application.BuildOutput{}, domain.Wrap(domain.CodeTimeout, "kpack build timed out", ctx.Err())
		case <-ticker.C:
		}
	}
}
func (a Adapter) Cancel(ctx context.Context, buildID string) error {
	if a.Client == nil {
		return nil
	}
	status, err := a.Client.Get(ctx, buildID)
	if err != nil {
		return err
	}
	if !status.Owned {
		return domain.NewError(domain.CodeConflict, "refusing to delete unowned kpack build")
	}
	return a.Client.Delete(ctx, buildID)
}

type statusError Status

func (s statusError) Error() string { return s.FailureReason + ": " + s.FailureMessage }

var _ application.Builder = Adapter{}
