package registry_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/keir-research/ai-native-paas/internal/build/application"
	"github.com/keir-research/ai-native-paas/internal/build/domain"
	"github.com/keir-research/ai-native-paas/internal/build/localoci"
	"github.com/keir-research/ai-native-paas/internal/build/logs"
	"github.com/keir-research/ai-native-paas/internal/build/memory"
	"github.com/keir-research/ai-native-paas/internal/build/registry"
	"github.com/keir-research/ai-native-paas/internal/build/testkit"
	buildv1 "github.com/keir-research/ai-native-paas/pkg/contracts/build/v1"
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
	subject := application.PublishedArtifact{Repository: repo, Digest: rawDigest([]byte("artifact")), MediaType: "application/vnd.oci.image.manifest.v1+json"}
	d1, err := r.StoreAttachment(context.Background(), "t1", subject, "application/spdx+json", []byte("sbom"))
	if err != nil {
		t.Fatal(err)
	}
	d2, err := r.StoreAttachment(context.Background(), "t1", subject, "application/vnd.dev.cosign.simplesigning.v1+json", []byte("sig"))
	if err != nil {
		t.Fatal(err)
	}
	if m, ok := r.AttachmentMediaType(subject, d1); !ok || m != "application/spdx+json" {
		t.Fatal(m, ok)
	}
	if _, ok := r.AttachmentMediaType(subject, d2); !ok {
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

type lostResponseRegistry struct {
	inner                      *registry.Local
	publishCalls, resolveCalls int
}

func (r *lostResponseRegistry) Publish(ctx context.Context, tenantID, repository string, output application.BuildOutput) (application.PublishedArtifact, error) {
	r.publishCalls++
	if _, err := r.inner.Publish(ctx, tenantID, repository, output); err != nil {
		return application.PublishedArtifact{}, err
	}
	return application.PublishedArtifact{}, domain.Retryable(domain.CodePlatformFailure, "registry response lost", errors.New("connection reset"))
}
func (r *lostResponseRegistry) Resolve(ctx context.Context, tenantID, reference string) (application.PublishedArtifact, error) {
	r.resolveCalls++
	return r.inner.Resolve(ctx, tenantID, reference)
}
func (r *lostResponseRegistry) StoreAttachment(ctx context.Context, tenantID string, subject application.PublishedArtifact, mediaType string, raw []byte) (string, error) {
	return r.inner.StoreAttachment(ctx, tenantID, subject, mediaType, raw)
}

func TestBuild_RegistryPushResponseLostRecoversByDigestDiscovery(t *testing.T) {
	output := makeOutput(t)
	registryWithLostResponse := &lostResponseRegistry{inner: registry.NewLocal(t.TempDir())}
	clock := &testkit.Clock{T: time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)}
	store := memory.New()
	provenanceAttestor, provenanceVerifier := testkit.ProvenanceFakes([]byte("provenance"))
	service := &application.Service{
		Store: store, Fetcher: &testkit.Fetcher{Snapshot: application.SourceSnapshot{Path: t.TempDir()}},
		Detector:   &testkit.Detector{Detection: application.Detection{Runtime: "go", Backend: application.BackendBuildpacks, BuildpackID: "paketo/go"}},
		Buildpacks: &testkit.Builder{Output: output}, Registry: registryWithLostResponse,
		SBOM:     testkit.SBOM{Result: application.SBOMResult{Digest: rawDigest([]byte("sbom")), MediaType: "application/spdx+json", Document: []byte("sbom")}},
		Scanner:  testkit.Scanner{Result: domain.ScanResult{Scanner: "scanner", PolicyVersion: "v1", Passed: true, FindingsDigest: "sha256:" + strings.Repeat("d", 64), ScannedAt: clock.Now()}},
		Signer:   testkit.Signer{Record: domain.SignatureRecord{Issuer: "platform", Algorithm: "ed25519", Digest: output.ManifestDigest, Signature: "signature", SignedAt: clock.Now()}},
		Verifier: &testkit.Verifier{}, Provenance: provenanceAttestor, ProvenanceVerifier: provenanceVerifier,
		Logs: logs.New(), Clock: clock, IDs: &testkit.IDs{}, RepositoryBase: "registry.test/tenants",
	}
	requested, err := service.RequestBuild(context.Background(), application.RequestBuildCommand{
		TenantID: "t1", ActorID: "u1", CorrelationID: "correlation-1", IdempotencyKey: "request-1",
		Source:        sourcev1.SourceRevision{ProjectID: "p1", RepositoryID: "r1", Branch: "main", CommitSHA: strings.Repeat("a", 40)},
		BuilderDigest: "sha256:" + strings.Repeat("b", 64), RunImageDigest: "sha256:" + strings.Repeat("c", 64), PlatformVersion: "v2",
	})
	if err != nil {
		t.Fatal(err)
	}
	completed, artifact, err := service.RunBuild(context.Background(), "t1", "u1", requested.Build.ID)
	if err != nil || completed.State != buildv1.BuildSucceeded || artifact == nil || artifact.Digest != output.ManifestDigest {
		t.Fatalf("build=%+v artifact=%+v err=%v", completed, artifact, err)
	}
	_, artifacts, outbox, audit := store.Snapshot()
	if registryWithLostResponse.publishCalls != 1 || registryWithLostResponse.resolveCalls != 1 || len(artifacts) != 1 {
		t.Fatalf("publish=%d resolve=%d artifacts=%d", registryWithLostResponse.publishCalls, registryWithLostResponse.resolveCalls, len(artifacts))
	}
	if !hasTopic(outbox, "build.registry_publish_recovered.v2") || !hasAction(audit, "registry.publish.recover") {
		t.Fatalf("outbox=%+v audit=%+v", outbox, audit)
	}
}

func rawDigest(raw []byte) string {
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func hasTopic(records []application.OutboxRecord, topic string) bool {
	for _, record := range records {
		if record.Topic == topic {
			return true
		}
	}
	return false
}

func hasAction(records []application.AuditRecord, action string) bool {
	for _, record := range records {
		if record.Action == action {
			return true
		}
	}
	return false
}
