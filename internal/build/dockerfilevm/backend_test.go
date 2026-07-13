package dockerfilevm_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/keir-research/ai-native-paas/internal/build/application"
	"github.com/keir-research/ai-native-paas/internal/build/dockerfilevm"
	"github.com/keir-research/ai-native-paas/internal/build/domain"
)

type manager struct {
	createCalls, buildCalls, destroyCalls int
	request                               dockerfilevm.VMRequest
	buildErr, destroyErr                  error
}

func (m *manager) Create(_ context.Context, r dockerfilevm.VMRequest) (dockerfilevm.VM, error) {
	m.createCalls++
	m.request = r
	return dockerfilevm.VM{ID: "vm-1"}, nil
}
func (m *manager) Build(context.Context, dockerfilevm.VM, dockerfilevm.VMRequest) (application.BuildOutput, error) {
	m.buildCalls++
	if m.buildErr != nil {
		return application.BuildOutput{}, m.buildErr
	}
	return application.BuildOutput{ManifestDigest: "sha256:" + strings.Repeat("a", 64), MediaType: "m"}, nil
}
func (m *manager) Destroy(context.Context, dockerfilevm.VM) error {
	m.destroyCalls++
	return m.destroyErr
}
func (m *manager) Cancel(context.Context, string) error { return nil }
func request() application.BuildExecutionRequest {
	return application.BuildExecutionRequest{BuildID: "b1", TenantID: "t1", Source: application.SourceSnapshot{Path: "/source"}, Config: domain.BuildConfig{Type: domain.BuildTypeDockerfile, DockerfilePath: "Dockerfile"}, Repository: "registry/tenants/t1/apps/p1"}
}
func TestDockerfileBuild_UsesIsolatedVMBackend(t *testing.T) {
	m := &manager{}
	out, err := (dockerfilevm.Backend{Manager: m}).Build(context.Background(), request())
	if err != nil || m.createCalls != 1 || m.buildCalls != 1 || out.ManifestDigest == "" {
		t.Fatalf("out=%+v m=%+v err=%v", out, m, err)
	}
}
func TestDockerfileBuild_HasNoRuntimeClusterCredential(t *testing.T) {
	m := &manager{}
	_, _ = (dockerfilevm.Backend{Manager: m}).Build(context.Background(), request())
	if m.request.RuntimeClusterCredential != "" {
		t.Fatal("runtime cluster credential leaked to VM")
	}
}
func TestDockerfileBuild_HasRestrictedEgress(t *testing.T) {
	m := &manager{}
	backend := dockerfilevm.Backend{Manager: m, AllowedHosts: []string{"git.example", "registry.example", "packages.example"}}
	_, _ = backend.Build(context.Background(), request())
	if !m.request.Network.DenyPrivateNetworks || !m.request.Network.DenyMetadata || len(m.request.Network.AllowedHosts) != 3 {
		t.Fatalf("policy=%+v", m.request.Network)
	}
}
func TestDockerfileBuild_VMIsDestroyedAfterSuccess(t *testing.T) {
	m := &manager{}
	_, err := (dockerfilevm.Backend{Manager: m}).Build(context.Background(), request())
	if err != nil || m.destroyCalls != 1 {
		t.Fatalf("destroy=%d err=%v", m.destroyCalls, err)
	}
}
func TestDockerfileBuild_VMIsDestroyedAfterFailure(t *testing.T) {
	m := &manager{buildErr: errors.New("builder crashed")}
	_, err := (dockerfilevm.Backend{Manager: m}).Build(context.Background(), request())
	if err == nil || m.destroyCalls != 1 || !domain.HasCode(err, domain.CodePlatformFailure) {
		t.Fatalf("destroy=%d err=%v", m.destroyCalls, err)
	}
}
