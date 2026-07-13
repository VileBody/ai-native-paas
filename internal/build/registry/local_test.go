package registry_test

import (
	"context"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/keir-research/ai-native-paas/internal/build/application"
	"github.com/keir-research/ai-native-paas/internal/build/domain"
	"github.com/keir-research/ai-native-paas/internal/build/localoci"
	"github.com/keir-research/ai-native-paas/internal/build/registry"
	sourcev1 "github.com/keir-research/ai-native-paas/pkg/contracts/source/v1"
)

func makeOutput(t *testing.T) application.BuildOutput {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "../../.."))
	source := filepath.Join(root, "test", "fixtures", "build", "hello-go")
	out, err := (localoci.Builder{Root: t.TempDir()}).Build(context.Background(), application.BuildExecutionRequest{BuildID: "b1", TenantID: "t1", Source: application.SourceSnapshot{Revision: sourcev1.SourceRevision{ProjectID: "p1", RepositoryID: "r1", Branch: "main", CommitSHA: strings.Repeat("a", 40)}, Path: source}, Detection: application.Detection{Runtime: "go", Backend: application.BackendBuildpacks}, Repository: "registry.test/tenants/t1/apps/p1"})
	if err != nil {
		t.Fatal(err)
	}
	return out
}
func TestRegistryAdapter_PushesToTenantRepository(t *testing.T) {
	r := registry.NewLocal(t.TempDir())
	published, err := r.Publish(context.Background(), "t1", "registry.test/tenants/t1/apps/p1", makeOutput(t))
	if err != nil || published.Repository != "registry.test/tenants/t1/apps/p1" || published.Digest == "" {
		t.Fatal(published, err)
	}
}
func TestRegistryAdapter_ResolvesDigest(t *testing.T) {
	r := registry.NewLocal(t.TempDir())
	published, err := r.Publish(context.Background(), "t1", "registry.test/tenants/t1/apps/p1", makeOutput(t))
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := r.Resolve(context.Background(), "t1", published.Repository+"@"+published.Digest)
	if err != nil || resolved != published {
		t.Fatalf("resolved=%+v published=%+v err=%v", resolved, published, err)
	}
}
func TestRegistryAdapter_DeniesCrossTenantRepository(t *testing.T) {
	r := registry.NewLocal(t.TempDir())
	_, err := r.Publish(context.Background(), "t2", "registry.test/tenants/t1/apps/p1", makeOutput(t))
	if !domain.HasCode(err, domain.CodeConflict) {
		t.Fatalf("err=%v", err)
	}
}
func TestRegistryAdapter_StoresSBOMAndSignatureReferences(t *testing.T) {
	r := registry.NewLocal(t.TempDir())
	repo := "registry.test/tenants/t1/apps/p1"
	d1, err := r.StoreAttachment(context.Background(), "t1", repo, "application/spdx+json", []byte("sbom"))
	if err != nil {
		t.Fatal(err)
	}
	d2, err := r.StoreAttachment(context.Background(), "t1", repo, "application/vnd.dev.cosign.simplesigning.v1+json", []byte("sig"))
	if err != nil {
		t.Fatal(err)
	}
	if m, ok := r.AttachmentMediaType(repo, d1); !ok || m != "application/spdx+json" {
		t.Fatal(m, ok)
	}
	if _, ok := r.AttachmentMediaType(repo, d2); !ok {
		t.Fatal("signature attachment missing")
	}
}

func TestRegistryAdapter_TenantOwnershipUsesExactPathSegments(t *testing.T) {
	r := registry.NewLocal(t.TempDir())
	for _, repository := range []string{
		"registry.test/tenants/not-t1/apps/p1",
		"registry.test/tenants/t11/apps/p1",
		"registry.test/prefix-tenants/t1/apps/p1",
		"registry.test/tenants/t1/not-apps/p1",
	} {
		if _, err := r.Publish(context.Background(), "t1", repository, makeOutput(t)); !domain.HasCode(err, domain.CodeConflict) {
			t.Errorf("repository=%q err=%v", repository, err)
		}
	}
}

func TestRegistryAdapter_RejectsInvalidBuilderDigestWithoutLayout(t *testing.T) {
	r := registry.NewLocal(t.TempDir())
	_, err := r.Publish(context.Background(), "t1", "registry.test/tenants/t1/apps/p1", application.BuildOutput{ManifestDigest: "latest"})
	if !domain.HasCode(err, domain.CodeInvalidArgument) {
		t.Fatalf("err=%v", err)
	}
}
