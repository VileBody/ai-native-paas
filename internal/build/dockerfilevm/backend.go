package dockerfilevm

import (
	"context"
	"errors"

	"github.com/keir-research/ai-native-paas/internal/build/application"
	"github.com/keir-research/ai-native-paas/internal/build/domain"
)

type NetworkPolicy struct {
	AllowedHosts        []string
	DenyPrivateNetworks bool
	DenyMetadata        bool
}
type VMRequest struct {
	BuildID, TenantID, SourcePath, DockerfilePath, Repository string
	Environment                                               map[string]string
	BuildArguments                                            map[string]string
	Secrets                                                   []application.BuildSecret
	Network                                                   NetworkPolicy
	ResourceClass                                             string
	TimeoutSeconds                                            int64
	RuntimeClusterCredential                                  string
}
type VM struct{ ID string }
type Manager interface {
	Create(context.Context, VMRequest) (VM, error)
	Build(context.Context, VM, VMRequest) (application.BuildOutput, error)
	Destroy(context.Context, VM) error
	Cancel(context.Context, string) error
}
type Backend struct {
	Manager      Manager
	AllowedHosts []string
}

func (b Backend) Build(ctx context.Context, request application.BuildExecutionRequest) (output application.BuildOutput, err error) {
	if b.Manager == nil {
		return output, domain.NewError(domain.CodeUnavailable, "disposable VM manager unavailable")
	}
	vmRequest := VMRequest{BuildID: request.BuildID, TenantID: request.TenantID, SourcePath: request.Source.Path, DockerfilePath: request.Config.DockerfilePath, Repository: request.Repository, Environment: cloneMap(request.Environment), Secrets: append([]application.BuildSecret(nil), request.Secrets...), Network: NetworkPolicy{AllowedHosts: append([]string(nil), b.AllowedHosts...), DenyPrivateNetworks: true, DenyMetadata: true}}
	if request.BuildSpec != nil {
		vmRequest.BuildArguments = cloneMap(request.BuildSpec.BuildArguments)
		vmRequest.ResourceClass = request.BuildSpec.ResourceClass
		vmRequest.TimeoutSeconds = request.BuildSpec.TimeoutSeconds
	}
	vm, err := b.Manager.Create(ctx, vmRequest)
	if err != nil {
		return output, domain.Wrap(domain.CodePlatformFailure, "create disposable build VM", err)
	}
	defer func() {
		destroyErr := b.Manager.Destroy(context.WithoutCancel(ctx), vm)
		if err == nil && destroyErr != nil {
			err = domain.Wrap(domain.CodePlatformFailure, "destroy disposable build VM", destroyErr)
		}
	}()
	output, err = b.Manager.Build(ctx, vm, vmRequest)
	if err != nil {
		var typed *domain.Error
		if errors.As(err, &typed) {
			return output, err
		}
		return output, domain.Wrap(domain.CodePlatformFailure, "Dockerfile build failed", err)
	}
	return output, nil
}
func (Backend) IsolationBoundary() application.IsolationBoundary {
	return application.IsolationDisposableWorkspaceVM
}
func (b Backend) Cancel(ctx context.Context, buildID string) error {
	if b.Manager == nil {
		return nil
	}
	return b.Manager.Cancel(ctx, buildID)
}
func cloneMap(in map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range in {
		out[k] = v
	}
	return out
}

var _ application.IsolatedBuilder = Backend{}
