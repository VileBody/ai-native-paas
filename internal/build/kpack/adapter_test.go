package kpack_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/keir-research/ai-native-paas/internal/build/application"
	"github.com/keir-research/ai-native-paas/internal/build/domain"
	"github.com/keir-research/ai-native-paas/internal/build/kpack"
	sourcev1 "github.com/keir-research/ai-native-paas/pkg/contracts/source/v1"
)

type client struct {
	mu                sync.Mutex
	spec              kpack.BuildSpec
	statuses          []kpack.Status
	createErr, getErr error
	deleted           []string
}

func (c *client) Create(_ context.Context, s kpack.BuildSpec) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.spec = s
	return c.createErr
}
func (c *client) Get(context.Context, string) (kpack.Status, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.getErr != nil {
		return kpack.Status{}, c.getErr
	}
	if len(c.statuses) == 0 {
		return kpack.Status{Phase: "RUNNING", Owned: true}, nil
	}
	s := c.statuses[0]
	if len(c.statuses) > 1 {
		c.statuses = c.statuses[1:]
	}
	return s, nil
}
func (c *client) Delete(_ context.Context, id string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.deleted = append(c.deleted, id)
	return nil
}
func req() application.BuildExecutionRequest {
	return application.BuildExecutionRequest{BuildID: "b1", TenantID: "t1", BuilderDigest: "sha256:" + strings.Repeat("b", 64), RunImageDigest: "sha256:" + strings.Repeat("c", 64), Source: application.SourceSnapshot{Revision: sourcev1.SourceRevision{ProjectID: "p1", RepositoryID: "r1", Branch: "main", CommitSHA: strings.Repeat("a", 40)}}, Detection: application.Detection{Backend: application.BackendBuildpacks, BuildpackID: "paketo/go"}, Repository: "registry/tenants/t1/apps/p1"}
}
func TestKpackAdapter_CreatesBuildForExactSourceRevision(t *testing.T) {
	c := &client{statuses: []kpack.Status{{Phase: "SUCCEEDED", ImageDigest: "sha256:" + strings.Repeat("d", 64), MediaType: "m"}}}
	_, err := (kpack.Adapter{Client: c, PollInterval: time.Millisecond}).Build(context.Background(), req())
	if err != nil {
		t.Fatal(err)
	}
	if c.spec.Source.CommitSHA != strings.Repeat("a", 40) || c.spec.Destination != "registry/tenants/t1/apps/p1" || c.spec.Labels["platform.example.com/build-id"] != "b1" {
		t.Fatalf("spec=%+v", c.spec)
	}
}
func TestKpackAdapter_MapsLifecycleStatus(t *testing.T) {
	c := &client{statuses: []kpack.Status{{Phase: "FAILED_USER_CODE", FailureReason: "COMPILE", FailureMessage: "syntax"}}}
	_, err := (kpack.Adapter{Client: c, PollInterval: time.Millisecond}).Build(context.Background(), req())
	if !domain.HasCode(err, domain.CodeUserFailure) {
		t.Fatalf("err=%v", err)
	}
	c = &client{statuses: []kpack.Status{{Phase: "FAILED_PLATFORM", FailureReason: "REGISTRY", FailureMessage: "timeout"}}}
	_, err = (kpack.Adapter{Client: c, PollInterval: time.Millisecond}).Build(context.Background(), req())
	if !domain.HasCode(err, domain.CodePlatformFailure) || !domain.IsRetryable(err) {
		t.Fatalf("err=%v", err)
	}
}
func TestKpackAdapter_CollectsImageDigest(t *testing.T) {
	want := "sha256:" + strings.Repeat("e", 64)
	c := &client{statuses: []kpack.Status{{Phase: "RUNNING"}, {Phase: "SUCCEEDED", ImageDigest: want, MediaType: "m"}}}
	out, err := (kpack.Adapter{Client: c, PollInterval: time.Millisecond}).Build(context.Background(), req())
	if err != nil || out.ManifestDigest != want {
		t.Fatalf("out=%+v err=%v", out, err)
	}
}
func TestKpackAdapter_CancelDeletesOnlyOwnedBuild(t *testing.T) {
	c := &client{statuses: []kpack.Status{{Phase: "RUNNING", Owned: false}}}
	a := kpack.Adapter{Client: c}
	if err := a.Cancel(context.Background(), "foreign"); !domain.HasCode(err, domain.CodeConflict) {
		t.Fatalf("err=%v", err)
	}
	c.statuses = []kpack.Status{{Phase: "RUNNING", Owned: true}}
	if err := a.Cancel(context.Background(), "owned"); err != nil {
		t.Fatal(err)
	}
	if len(c.deleted) != 1 || c.deleted[0] != "owned" {
		t.Fatal(c.deleted)
	}
}
func TestKpackAdapter_RegistryTimeoutIsRetryable(t *testing.T) {
	c := &client{getErr: errors.New("registry timeout")}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := (kpack.Adapter{Client: c, PollInterval: time.Millisecond}).Build(ctx, req())
	if !domain.HasCode(err, domain.CodePlatformFailure) || !domain.IsRetryable(err) {
		t.Fatalf("err=%v", err)
	}
}
