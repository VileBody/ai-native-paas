package application_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/keir-research/ai-native-paas/internal/build/application"
	"github.com/keir-research/ai-native-paas/internal/build/domain"
	"github.com/keir-research/ai-native-paas/internal/build/logs"
	"github.com/keir-research/ai-native-paas/internal/build/memory"
	"github.com/keir-research/ai-native-paas/internal/build/testkit"
	buildv1 "github.com/keir-research/ai-native-paas/pkg/contracts/build/v1"
	buildv2 "github.com/keir-research/ai-native-paas/pkg/contracts/build/v2"
	sourcev1 "github.com/keir-research/ai-native-paas/pkg/contracts/source/v1"
)

func dg(ch string) string { return "sha256:" + strings.Repeat(ch, 64) }
func rawDigest(raw []byte) string {
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}
func setup(t *testing.T) (*application.Service, *testkit.Builder, *testkit.Registry, *logs.Memory, *testkit.Clock) {
	t.Helper()
	path := t.TempDir()
	_ = os.WriteFile(path+"/app.txt", []byte("safe"), 0644)
	_ = os.WriteFile(path+"/Dockerfile", []byte("FROM scratch\n"), 0644)
	clock := &testkit.Clock{T: time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)}
	builder := &testkit.Builder{Output: application.BuildOutput{ManifestDigest: dg("a"), MediaType: "application/vnd.oci.image.manifest.v1+json"}}
	registry := &testkit.Registry{Published: application.PublishedArtifact{Repository: "registry.test/tenants/t1/apps/p1", Digest: dg("a"), MediaType: "application/vnd.oci.image.manifest.v1+json"}}
	logStore := logs.New()
	service := &application.Service{Store: memory.New(), Fetcher: &testkit.Fetcher{Snapshot: application.SourceSnapshot{Path: path}}, Detector: &testkit.Detector{Detection: application.Detection{Runtime: "go", Backend: application.BackendBuildpacks, BuildpackID: "paketo/go"}}, Buildpacks: builder, Registry: registry, SBOM: testkit.SBOM{Result: application.SBOMResult{Digest: rawDigest([]byte("sbom")), MediaType: "application/spdx+json", Document: []byte("sbom")}}, Scanner: testkit.Scanner{Result: domain.ScanResult{Scanner: "test", PolicyVersion: "v1", Passed: true, FindingsDigest: dg("c"), ScannedAt: clock.Now()}}, Signer: testkit.Signer{Record: domain.SignatureRecord{Issuer: "platform", Algorithm: "ed25519", Digest: dg("a"), Signature: "signature", SignedAt: clock.Now()}}, Verifier: &testkit.Verifier{}, Logs: logStore, Clock: clock, IDs: &testkit.IDs{}, RepositoryBase: "registry.test/tenants"}
	return service, builder, registry, logStore, clock
}
func command(key string) application.RequestBuildCommand {
	return application.RequestBuildCommand{TenantID: "t1", ActorID: "u1", CorrelationID: "cor-1", IdempotencyKey: key, Source: sourcev1.SourceRevision{ProjectID: "p1", RepositoryID: "r1", Branch: "main", CommitSHA: strings.Repeat("d", 40)}, Config: domain.BuildConfig{}, BuilderDigest: dg("e"), RunImageDigest: dg("f"), PlatformVersion: "v1"}
}

func explicitDockerfileCommand(key string) application.RequestBuildV2Command {
	sha := strings.Repeat("d", 40)
	spec := &buildv2.BuildSpec{
		SourceSHA: sha, Driver: buildv2.DriverDockerfile, DefinitionPath: "Dockerfile",
		Platforms: []string{"linux/amd64"}, NetworkProfile: "governed",
		CacheScope: "project-p1", ResourceClass: "standard", TimeoutSeconds: 900,
	}
	return application.RequestBuildV2Command{
		TenantID: "t1", ActorID: "u1", CorrelationID: "cor-v2", IdempotencyKey: key,
		Source: sourcev1.SourceRevision{ProjectID: "p1", RepositoryID: "r1", Branch: "main", CommitSHA: sha}, Spec: spec,
		BuilderDigest: dg("e"), RunImageDigest: dg("f"), PlatformVersion: "v2",
	}
}

func TestBuild_ExplicitBuildSpecOverridesRuntimeDetection(t *testing.T) {
	s, _, _, _, _ := setup(t)
	detector := s.Detector.(*testkit.Detector)
	detector.Detection = application.Detection{Runtime: "go", Backend: application.BackendBuildpacks, BuildpackID: "paketo/go", Evidence: []string{"go.mod", "package.json"}}
	dockerfile := &testkit.Builder{Output: application.BuildOutput{ManifestDigest: dg("a"), MediaType: "application/vnd.oci.image.manifest.v1+json"}}
	s.Dockerfile = &testkit.IsolatedBuilder{Builder: dockerfile, Isolation: application.IsolationDisposableWorkspaceVM}

	requested, err := s.RequestBuildV2(context.Background(), explicitDockerfileCommand("explicit-dockerfile"))
	if err != nil {
		t.Fatal(err)
	}
	build, _, err := s.RunBuild(context.Background(), "t1", "u1", requested.Build.ID)
	if err != nil {
		t.Fatal(err)
	}
	if detector.Calls != 0 || build.Backend != domain.ExecutionDockerfile || build.BuildSpec == nil || build.BuildSpecDigest == "" {
		t.Fatalf("detector calls=%d build=%+v", detector.Calls, build)
	}
	request, ok := dockerfile.LastRequest()
	if !ok || request.BuildSpec == nil || request.BuildSpec.Driver != buildv2.DriverDockerfile || request.Detection.Backend != application.BackendDockerfile {
		t.Fatalf("request=%+v ok=%v", request, ok)
	}
}

func TestBuild_BuildpacksRemainOptionalFallback(t *testing.T) {
	s, buildpacks, _, _, _ := setup(t)
	detector := s.Detector.(*testkit.Detector)
	detector.Detection.Evidence = []string{"go.mod"}
	cmd := explicitDockerfileCommand("auto-buildpacks")
	cmd.Spec = nil
	cmd.AllowAutoDetection = true

	requested, err := s.RequestBuildV2(context.Background(), cmd)
	if err != nil {
		t.Fatal(err)
	}
	build, _, err := s.RunBuild(context.Background(), "t1", "u1", requested.Build.ID)
	if err != nil {
		t.Fatal(err)
	}
	if detector.Calls != 1 || build.Backend != domain.ExecutionBuildpacks || build.BuildpackID != "paketo/go" || len(buildpacks.Requests) != 1 {
		t.Fatalf("detector calls=%d build=%+v requests=%d", detector.Calls, build, len(buildpacks.Requests))
	}
	persisted, _, err := s.GetBuild(context.Background(), "t1", build.ID)
	if err != nil || persisted.Backend != domain.ExecutionBuildpacks || persisted.BuildpackID != "paketo/go" || !persisted.AutoDetectionAllowed {
		t.Fatalf("persisted=%+v err=%v", persisted, err)
	}
	_, _, _, audit := s.Store.(*memory.Store).Snapshot()
	var selection map[string]any
	for _, record := range audit {
		if record.Action == "build.execution.select" && record.ResourceID == build.ID {
			if err := json.Unmarshal(record.Data, &selection); err != nil {
				t.Fatal(err)
			}
		}
	}
	if selection["selection_source"] != "runtime-detector" || selection["backend"] != string(application.BackendBuildpacks) {
		t.Fatalf("selection audit=%v", selection)
	}
}

func TestBuild_DockerfileRunsOnlyInDisposableIsolationBackend(t *testing.T) {
	s, _, _, _, _ := setup(t)
	unisolated := &testkit.Builder{Output: application.BuildOutput{ManifestDigest: dg("a"), MediaType: "application/vnd.oci.image.manifest.v1+json"}}
	s.Dockerfile = unisolated
	requested, err := s.RequestBuildV2(context.Background(), explicitDockerfileCommand("reject-shared-builder"))
	if err != nil {
		t.Fatal(err)
	}
	build, _, err := s.RunBuild(context.Background(), "t1", "u1", requested.Build.ID)
	if !domain.HasCode(err, domain.CodePolicyRejected) || build.Backend != domain.ExecutionDockerfile || build.State != buildv1.BuildFailedUserCode {
		t.Fatalf("build=%+v err=%v", build, err)
	}
	if len(unisolated.Requests) != 0 {
		t.Fatalf("unisolated Dockerfile builder was invoked: %+v", unisolated.Requests)
	}
}

func TestBuildV2_RequiresExclusiveExplicitOrBuildpacksFallback(t *testing.T) {
	s, _, _, _, _ := setup(t)
	missing := explicitDockerfileCommand("missing-mode")
	missing.Spec = nil
	if _, err := s.RequestBuildV2(context.Background(), missing); !domain.HasCode(err, domain.CodeInvalidArgument) {
		t.Fatalf("missing mode err=%v", err)
	}
	both := explicitDockerfileCommand("both-modes")
	both.AllowAutoDetection = true
	if _, err := s.RequestBuildV2(context.Background(), both); !domain.HasCode(err, domain.CodeInvalidArgument) {
		t.Fatalf("both modes err=%v", err)
	}

	detector := s.Detector.(*testkit.Detector)
	detector.Detection = application.Detection{Runtime: "dockerfile", Backend: application.BackendDockerfile, Evidence: []string{"Dockerfile"}}
	implicitDockerfile := explicitDockerfileCommand("implicit-dockerfile")
	implicitDockerfile.Spec = nil
	implicitDockerfile.AllowAutoDetection = true
	requested, err := s.RequestBuildV2(context.Background(), implicitDockerfile)
	if err != nil {
		t.Fatal(err)
	}
	build, _, err := s.RunBuild(context.Background(), "t1", "u1", requested.Build.ID)
	if !domain.HasCode(err, domain.CodePolicyRejected) || build.State != buildv1.BuildFailedUserCode || build.Backend != "" {
		t.Fatalf("build=%+v err=%v", build, err)
	}
}

func TestBuildV2_MutableDockerfileIsRejectedBeforeDisposableVM(t *testing.T) {
	s, _, _, _, _ := setup(t)
	path := s.Fetcher.(*testkit.Fetcher).Snapshot.Path
	if err := os.WriteFile(path+"/Dockerfile", []byte("FROM ubuntu:latest\n"), 0600); err != nil {
		t.Fatal(err)
	}
	dockerfile := &testkit.Builder{Output: application.BuildOutput{ManifestDigest: dg("a"), MediaType: "application/vnd.oci.image.manifest.v1+json"}}
	s.Dockerfile = &testkit.IsolatedBuilder{Builder: dockerfile, Isolation: application.IsolationDisposableWorkspaceVM}
	requested, err := s.RequestBuildV2(context.Background(), explicitDockerfileCommand("mutable-base"))
	if err != nil {
		t.Fatal(err)
	}
	build, _, err := s.RunBuild(context.Background(), "t1", "u1", requested.Build.ID)
	if !domain.HasCode(err, domain.CodePolicyRejected) || build.State != buildv1.BuildFailedUserCode || build.Backend != "" {
		t.Fatalf("build=%+v err=%v", build, err)
	}
	if len(dockerfile.Requests) != 0 {
		t.Fatalf("mutable Dockerfile reached VM backend: %+v", dockerfile.Requests)
	}
}

func TestBuildReceipt_IngestsWorkspaceDigestAndPersistsFullTrustChain(t *testing.T) {
	s, builder, registry, _, clock := setup(t)
	provenanceDocument := []byte(`{"payload":"signed-provenance"}`)
	provenanceResult := application.ProvenanceResult{Digest: rawDigest(provenanceDocument), MediaType: "application/vnd.dsse.envelope.v1+json", Document: provenanceDocument}
	attestor := &testkit.ProvenanceAttestor{Result: provenanceResult}
	verifier := &testkit.ProvenanceVerifier{Result: provenanceResult}
	s.Provenance = attestor
	s.ProvenanceVerifier = verifier

	requested, err := s.RequestBuildV2(context.Background(), explicitDockerfileCommand("receipt-success"))
	if err != nil {
		t.Fatal(err)
	}
	receipt := buildv2.VerifiedBuildReceipt{
		SourceSHA: requested.Build.Source.CommitSHA, SpecDigest: requested.Build.BuildSpecDigest,
		Repository: "registry.test/tenants/t1/apps/p1", Digest: dg("a"),
		MediaType: "application/vnd.oci.image.manifest.v1+json", CapturedAt: clock.Now(),
		Builder: "rootless-buildkit", BuilderAddr: "unix:///run/workspace-buildkit/buildkitd.sock",
	}
	result, err := s.IngestVerifiedBuildReceipt(context.Background(), application.IngestVerifiedBuildReceiptCommand{
		TenantID: "t1", ActorID: "workspace-agent", IdempotencyKey: "receipt-1",
		BuildID: requested.Build.ID, Receipt: receipt,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Build.State != buildv1.BuildSucceeded || result.Artifact == nil || result.Artifact.State != domain.ArtifactReleasable {
		t.Fatalf("result=%+v", result)
	}
	if result.TrustChain == nil || result.TrustChain.Validate() != nil || result.TrustChain.ProvenanceDigest != provenanceResult.Digest {
		t.Fatalf("trust chain=%+v", result.TrustChain)
	}
	if len(builder.Requests) != 0 || registry.PublishCalls != 0 || registry.ResolveCalls != 1 {
		t.Fatalf("builder=%d publish=%d resolve=%d", len(builder.Requests), registry.PublishCalls, registry.ResolveCalls)
	}
	for _, mediaType := range []string{"application/spdx+json", "application/vnd.dev.cosign.simplesigning.v1+json", provenanceResult.MediaType} {
		if _, ok := registry.Attachments[mediaType]; !ok {
			t.Fatalf("attachment %s missing: %+v", mediaType, registry.Attachments)
		}
	}
	if len(attestor.Materials) != 1 || attestor.Materials[0].BuildSpecDigest != requested.Build.BuildSpecDigest || attestor.Materials[0].OutputDigest != dg("a") {
		t.Fatalf("provenance materials=%+v", attestor.Materials)
	}
	again, err := s.IngestVerifiedBuildReceipt(context.Background(), application.IngestVerifiedBuildReceiptCommand{
		TenantID: "t1", ActorID: "workspace-agent", IdempotencyKey: "receipt-1",
		BuildID: requested.Build.ID, Receipt: receipt,
	})
	if err != nil || again.TrustChain == nil || again.TrustChain.ProvenanceDigest != provenanceResult.Digest {
		t.Fatalf("idempotent result=%+v err=%v", again, err)
	}
}

func TestBuildReceipt_RejectsSpoofedSpecBeforeRegistryAccess(t *testing.T) {
	s, _, registry, _, clock := setup(t)
	provenanceDocument := []byte(`{"payload":"signed-provenance"}`)
	provenanceResult := application.ProvenanceResult{Digest: rawDigest(provenanceDocument), MediaType: "application/vnd.dsse.envelope.v1+json", Document: provenanceDocument}
	s.Provenance = &testkit.ProvenanceAttestor{Result: provenanceResult}
	s.ProvenanceVerifier = &testkit.ProvenanceVerifier{Result: provenanceResult}
	requested, err := s.RequestBuildV2(context.Background(), explicitDockerfileCommand("receipt-spoof"))
	if err != nil {
		t.Fatal(err)
	}
	receipt := buildv2.VerifiedBuildReceipt{
		SourceSHA: requested.Build.Source.CommitSHA, SpecDigest: dg("9"),
		Repository: "registry.test/tenants/t1/apps/p1", Digest: dg("a"),
		MediaType: "application/vnd.oci.image.manifest.v1+json", CapturedAt: clock.Now(),
		Builder: "rootless-buildkit",
	}
	if _, err := s.IngestVerifiedBuildReceipt(context.Background(), application.IngestVerifiedBuildReceiptCommand{TenantID: "t1", ActorID: "workspace-agent", IdempotencyKey: "receipt-spoof-ingest", BuildID: requested.Build.ID, Receipt: receipt}); !domain.HasCode(err, domain.CodePolicyRejected) {
		t.Fatalf("err=%v", err)
	}
	if registry.ResolveCalls != 0 || registry.PublishCalls != 0 {
		t.Fatalf("registry was touched: publish=%d resolve=%d", registry.PublishCalls, registry.ResolveCalls)
	}
}

func TestBuildReceipt_FailsClosedWithoutProvenanceAdapters(t *testing.T) {
	s, _, registry, _, clock := setup(t)
	requested, err := s.RequestBuildV2(context.Background(), explicitDockerfileCommand("receipt-no-provenance"))
	if err != nil {
		t.Fatal(err)
	}
	receipt := buildv2.VerifiedBuildReceipt{
		SourceSHA: requested.Build.Source.CommitSHA, SpecDigest: requested.Build.BuildSpecDigest,
		Repository: "registry.test/tenants/t1/apps/p1", Digest: dg("a"),
		MediaType: "application/vnd.oci.image.manifest.v1+json", CapturedAt: clock.Now(),
		Builder: "rootless-buildkit",
	}
	if _, err := s.IngestVerifiedBuildReceipt(context.Background(), application.IngestVerifiedBuildReceiptCommand{TenantID: "t1", ActorID: "workspace-agent", IdempotencyKey: "receipt-no-provenance", BuildID: requested.Build.ID, Receipt: receipt}); !domain.HasCode(err, domain.CodeUnavailable) {
		t.Fatalf("err=%v", err)
	}
	if registry.ResolveCalls != 0 {
		t.Fatalf("registry was touched before provenance wiring: resolve=%d", registry.ResolveCalls)
	}
}

func TestBuildRequest_ConcurrentDuplicateCreatesOneBuild(t *testing.T) {
	s, _, _, _, _ := setup(t)
	const workers = 32
	var wg sync.WaitGroup
	ids := make(chan string, workers)
	errs := make(chan error, workers)
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func() {
			defer wg.Done()
			r, err := s.RequestBuild(context.Background(), command("same-key"))
			if err != nil {
				errs <- err
				return
			}
			ids <- r.Build.ID
		}()
	}
	wg.Wait()
	close(ids)
	close(errs)
	for err := range errs {
		t.Errorf("request: %v", err)
	}
	seen := map[string]bool{}
	for id := range ids {
		seen[id] = true
	}
	if len(seen) != 1 {
		t.Fatalf("builds=%v", seen)
	}
}
func TestBuildRequest_DuplicateReusesExistingSuccessfulArtifact(t *testing.T) {
	s, _, _, _, _ := setup(t)
	first, err := s.RequestBuild(context.Background(), command("k1"))
	if err != nil {
		t.Fatal(err)
	}
	build, artifact, err := s.RunBuild(context.Background(), "t1", "u1", first.Build.ID)
	if err != nil || build.State != buildv1.BuildSucceeded || artifact == nil {
		t.Fatalf("build=%+v artifact=%+v err=%v", build, artifact, err)
	}
	second, err := s.RequestBuild(context.Background(), command("k2"))
	if err != nil || !second.Reused || second.Build.ID != build.ID || second.Artifact == nil {
		t.Fatalf("second=%+v err=%v", second, err)
	}
}
func TestBuild_RuntimeSecretsAreUnavailable(t *testing.T) {
	s, b, _, _, _ := setup(t)
	s.SecretProvider = testkit.SecretProvider{BuildSecrets: []application.BuildSecret{{Name: "npm", Value: "build-only", AllowedPhases: []application.BuildPhase{application.PhaseBuild}}}, RuntimeSecret: "runtime-super-secret"}
	cmd := command("k1")
	cmd.Config.BuildSecretRef = []string{"npm"}
	requested, _ := s.RequestBuild(context.Background(), cmd)
	_, _, err := s.RunBuild(context.Background(), "t1", "u1", requested.Build.ID)
	if err != nil {
		t.Fatal(err)
	}
	captured, _ := b.LastRequest()
	for _, secret := range captured.Secrets {
		if secret.Value == "runtime-super-secret" {
			t.Fatal("runtime secret exposed to build")
		}
	}
}
func TestBuild_BuildSecretMountedOnlyForAllowedPhase(t *testing.T) {
	s, b, _, _, _ := setup(t)
	s.SecretProvider = testkit.SecretProvider{BuildSecrets: []application.BuildSecret{{Name: "detect-only", Value: "d", AllowedPhases: []application.BuildPhase{application.PhaseDetect}}, {Name: "build-only", Value: "b", AllowedPhases: []application.BuildPhase{application.PhaseBuild}}}}
	cmd := command("k1")
	cmd.Config.BuildSecretRef = []string{"detect-only", "build-only"}
	requested, _ := s.RequestBuild(context.Background(), cmd)
	_, _, err := s.RunBuild(context.Background(), "t1", "u1", requested.Build.ID)
	if err != nil {
		t.Fatal(err)
	}
	captured, _ := b.LastRequest()
	if len(captured.Secrets) != 1 || captured.Secrets[0].Name != "build-only" {
		t.Fatalf("secrets=%+v", captured.Secrets)
	}
}
func TestBuild_BuildSecretIsAbsentFromLogs(t *testing.T) {
	s, b, _, logStore, _ := setup(t)
	b.Output.Metadata = map[string]string{"note": "not-a-secret"}
	s.SecretProvider = testkit.SecretProvider{BuildSecrets: []application.BuildSecret{{Name: "npm", Value: "super-secret", AllowedPhases: []application.BuildPhase{application.PhaseBuild}}}}
	cmd := command("k1")
	cmd.Config.BuildSecretRef = []string{"npm"}
	requested, _ := s.RequestBuild(context.Background(), cmd)
	_, _, err := s.RunBuild(context.Background(), "t1", "u1", requested.Build.ID)
	if err != nil {
		t.Fatal(err)
	}
	writer := logStore.Writer(requested.Build.ID, []string{"super-secret"})
	_, _ = writer.Write([]byte("echo super-secret"))
	raw, _ := logStore.Read(context.Background(), requested.Build.ID)
	if strings.Contains(string(raw), "super-secret") {
		t.Fatalf("logs=%s", raw)
	}
}
func TestBuild_DependencyRegistryTimeoutIsRetryablePlatformFailure(t *testing.T) {
	s, _, registry, _, _ := setup(t)
	registry.PublishErr = domain.Retryable(domain.CodePlatformFailure, "registry timeout", errors.New("timeout"))
	registry.ResolveErr = domain.NewError(domain.CodeNotFound, "artifact not found")
	requested, _ := s.RequestBuild(context.Background(), command("k1"))
	build, _, err := s.RunBuild(context.Background(), "t1", "u1", requested.Build.ID)
	if err == nil || build.State != buildv1.BuildFailedPlatform || !build.Retryable {
		t.Fatalf("build=%+v err=%v", build, err)
	}
}
func TestBuild_BuilderCrashIsPlatformFailure(t *testing.T) {
	s, b, _, _, _ := setup(t)
	b.Err = errors.New("panic in builder")
	requested, _ := s.RequestBuild(context.Background(), command("k1"))
	build, _, err := s.RunBuild(context.Background(), "t1", "u1", requested.Build.ID)
	if err == nil || build.State != buildv1.BuildFailedPlatform || build.Retryable {
		t.Fatalf("build=%+v err=%v", build, err)
	}
}
func TestBuild_PolicyViolationRejectsArtifactAndBuild(t *testing.T) {
	s, _, _, _, clock := setup(t)
	s.Scanner = testkit.Scanner{Result: domain.ScanResult{Scanner: "test", PolicyVersion: "v1", Passed: false, FindingsDigest: dg("c"), Reasons: []string{"critical"}, ScannedAt: clock.Now()}}
	requested, _ := s.RequestBuild(context.Background(), command("k1"))
	build, artifact, err := s.RunBuild(context.Background(), "t1", "u1", requested.Build.ID)
	if err == nil || build.State != buildv1.BuildFailedUserCode || artifact == nil || artifact.State != domain.ArtifactRejected {
		t.Fatalf("build=%+v artifact=%+v err=%v", build, artifact, err)
	}
}
func TestBuild_ScanOperationalFailureLeavesArtifactQuarantined(t *testing.T) {
	s, _, _, _, _ := setup(t)
	s.Scanner = testkit.Scanner{Err: domain.Retryable(domain.CodePlatformFailure, "scanner down", errors.New("down"))}
	requested, _ := s.RequestBuild(context.Background(), command("k1"))
	build, artifact, err := s.RunBuild(context.Background(), "t1", "u1", requested.Build.ID)
	if err == nil || build.State != buildv1.BuildFailedPlatform || artifact == nil || artifact.State != domain.ArtifactQuarantined {
		t.Fatalf("build=%+v artifact=%+v err=%v", build, artifact, err)
	}
}

func TestService_RetryBuildCreatesNextAttemptAndPreservesCorrelationChain(t *testing.T) {
	s, builder, _, _, _ := setup(t)
	builder.Err = errors.New("builder crashed")
	requested, err := s.RequestBuild(context.Background(), command("initial"))
	if err != nil {
		t.Fatal(err)
	}
	failed, _, err := s.RunBuild(context.Background(), "t1", "u1", requested.Build.ID)
	if err == nil || failed.State != buildv1.BuildFailedPlatform {
		t.Fatalf("failed=%+v err=%v", failed, err)
	}
	retried, err := s.RetryBuild(context.Background(), application.RetryBuildCommand{TenantID: "t1", ActorID: "u1", CorrelationID: "cor-retry", IdempotencyKey: "retry-1", BuildID: failed.ID})
	if err != nil {
		t.Fatal(err)
	}
	if retried.Build.Attempt != 2 || retried.Build.OriginalCorrelationID != failed.OriginalCorrelationID || retried.Build.CorrelationID != "cor-retry" {
		t.Fatalf("retry=%+v", retried.Build)
	}
	again, err := s.RetryBuild(context.Background(), application.RetryBuildCommand{TenantID: "t1", ActorID: "u1", CorrelationID: "cor-retry", IdempotencyKey: "retry-1", BuildID: failed.ID})
	if err != nil || again.Build.ID != retried.Build.ID {
		t.Fatalf("again=%+v err=%v", again, err)
	}
}

func TestService_RetryBuildRejectsSuccessfulBuild(t *testing.T) {
	s, _, _, _, _ := setup(t)
	requested, _ := s.RequestBuild(context.Background(), command("initial"))
	succeeded, _, err := s.RunBuild(context.Background(), "t1", "u1", requested.Build.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.RetryBuild(context.Background(), application.RetryBuildCommand{TenantID: "t1", ActorID: "u1", CorrelationID: "retry", IdempotencyKey: "retry", BuildID: succeeded.ID})
	if !domain.HasCode(err, domain.CodeConflict) {
		t.Fatalf("err=%v", err)
	}
}

func TestService_GetLogsAndEvaluateArtifactAreTenantScoped(t *testing.T) {
	s, _, _, _, _ := setup(t)
	requested, _ := s.RequestBuild(context.Background(), command("initial"))
	_, artifact, err := s.RunBuild(context.Background(), "t1", "u1", requested.Build.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.GetBuild(context.Background(), "other", requested.Build.ID); !domain.HasCode(err, domain.CodeNotFound) {
		t.Fatalf("cross-tenant get err=%v", err)
	}
	if _, err := s.StreamBuildLogs(context.Background(), "other", requested.Build.ID); !domain.HasCode(err, domain.CodeNotFound) {
		t.Fatalf("cross-tenant logs err=%v", err)
	}
	decision, err := s.EvaluateArtifact(context.Background(), "t1", artifact.ID)
	if err != nil || !decision.Allowed {
		t.Fatalf("decision=%+v err=%v", decision, err)
	}
	if _, err := s.EvaluateArtifact(context.Background(), "other", artifact.ID); !domain.HasCode(err, domain.CodeNotFound) {
		t.Fatalf("cross-tenant artifact err=%v", err)
	}
}

type faultOutboxStore struct {
	inner     application.Store
	failTopic string
}

func (s faultOutboxStore) Transact(ctx context.Context, fn func(application.Tx) error) error {
	return s.inner.Transact(ctx, func(tx application.Tx) error {
		return fn(faultOutboxTx{Tx: tx, failTopic: s.failTopic})
	})
}

type faultOutboxTx struct {
	application.Tx
	failTopic string
}

func (tx faultOutboxTx) AppendOutbox(record application.OutboxRecord) error {
	if record.Topic == tx.failTopic {
		return errors.New("injected outbox failure")
	}
	return tx.Tx.AppendOutbox(record)
}

func TestBuild_SuccessEmitsFrozenLifecycleEvents(t *testing.T) {
	s, _, _, _, _ := setup(t)
	requested, err := s.RequestBuild(context.Background(), command("events"))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.RunBuild(context.Background(), "t1", "u1", requested.Build.ID); err != nil {
		t.Fatal(err)
	}
	_, _, outbox, _ := s.Store.(*memory.Store).Snapshot()
	seen := map[string]int{}
	for _, event := range outbox {
		seen[event.Topic]++
	}
	for _, topic := range []string{"build.requested.v1", "build.started.v1", "build.completed.v1", "artifact.releasable.v1"} {
		if seen[topic] != 1 {
			t.Fatalf("topic %s count=%d all=%v", topic, seen[topic], seen)
		}
	}
}

func TestBuild_RejectionEmitsArtifactRejectedEvent(t *testing.T) {
	s, _, _, _, clock := setup(t)
	s.Scanner = testkit.Scanner{Result: domain.ScanResult{Scanner: "test", PolicyVersion: "v1", Passed: false, FindingsDigest: dg("c"), Reasons: []string{"critical"}, ScannedAt: clock.Now()}}
	requested, _ := s.RequestBuild(context.Background(), command("rejected-events"))
	_, _, _ = s.RunBuild(context.Background(), "t1", "u1", requested.Build.ID)
	_, _, outbox, _ := s.Store.(*memory.Store).Snapshot()
	seenRejected := false
	for _, event := range outbox {
		if event.Topic == "artifact.rejected.v1" {
			seenRejected = true
		}
	}
	if !seenRejected {
		t.Fatalf("artifact.rejected.v1 missing: %+v", outbox)
	}
}

func TestBuild_TrustStateAndCompletionEventsCommitAtomically(t *testing.T) {
	s, _, _, _, _ := setup(t)
	base := s.Store.(*memory.Store)
	s.Store = faultOutboxStore{inner: base, failTopic: "artifact.releasable.v1"}
	requested, err := s.RequestBuild(context.Background(), command("atomic-completion"))
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = s.RunBuild(context.Background(), "t1", "u1", requested.Build.ID)
	if err == nil || !strings.Contains(err.Error(), "injected outbox failure") {
		t.Fatalf("err=%v", err)
	}
	var persisted domain.Build
	var artifact domain.Artifact
	if err := base.Transact(context.Background(), func(tx application.Tx) error {
		var ok bool
		persisted, ok = tx.GetBuild(requested.Build.ID)
		if !ok {
			return errors.New("build missing")
		}
		artifact, ok = tx.FindArtifactByBuild(requested.Build.ID)
		if !ok {
			return errors.New("artifact missing")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if persisted.State == buildv1.BuildSucceeded || artifact.State == domain.ArtifactReleasable || artifact.Signature != nil {
		t.Fatalf("partial trust escaped transaction: build=%s artifact=%s signature=%+v", persisted.State, artifact.State, artifact.Signature)
	}
}

func TestBuild_StartStateAndStartedEventCommitAtomically(t *testing.T) {
	s, _, _, _, _ := setup(t)
	base := s.Store.(*memory.Store)
	s.Store = faultOutboxStore{inner: base, failTopic: "build.started.v1"}
	requested, err := s.RequestBuild(context.Background(), command("atomic-start"))
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = s.RunBuild(context.Background(), "t1", "u1", requested.Build.ID)
	if err == nil {
		t.Fatal("expected start transaction failure")
	}
	var persisted domain.Build
	_ = base.Transact(context.Background(), func(tx application.Tx) error { persisted, _ = tx.GetBuild(requested.Build.ID); return nil })
	if persisted.State != buildv1.BuildQueued {
		t.Fatalf("state advanced without started event: %s", persisted.State)
	}
}

func persistBuildMutation(t *testing.T, store application.Store, build *domain.Build, mutate func(*domain.Build) error) {
	t.Helper()
	expected := build.Version
	if err := mutate(build); err != nil {
		t.Fatal(err)
	}
	if err := store.Transact(context.Background(), func(tx application.Tx) error {
		return tx.UpdateBuild(*build, expected)
	}); err != nil {
		t.Fatal(err)
	}
}

func TestService_CancelBuildUsesPersistedDockerfileBackend(t *testing.T) {
	s, buildpacks, _, _, clock := setup(t)
	dockerfile := &testkit.Builder{}
	s.Dockerfile = dockerfile
	requested, err := s.RequestBuild(context.Background(), command("cancel-dockerfile"))
	if err != nil {
		t.Fatal(err)
	}
	build := requested.Build
	persistBuildMutation(t, s.Store, &build, func(value *domain.Build) error {
		return value.Transition(buildv1.BuildFetchingSource, clock.Now())
	})
	persistBuildMutation(t, s.Store, &build, func(value *domain.Build) error {
		return value.Transition(buildv1.BuildDetecting, clock.Now())
	})
	persistBuildMutation(t, s.Store, &build, func(value *domain.Build) error {
		return value.SelectExecution("dockerfile", domain.ExecutionDockerfile, "", clock.Now())
	})

	canceled, err := s.CancelBuild(context.Background(), "t1", build.ID)
	if err != nil {
		t.Fatal(err)
	}
	if canceled.State != buildv1.BuildCanceled || canceled.Backend != domain.ExecutionDockerfile {
		t.Fatalf("build=%+v", canceled)
	}
	if dockerfile.CancelCount() != 1 || buildpacks.CancelCount() != 0 {
		t.Fatalf("dockerfile cancels=%d buildpacks cancels=%d", dockerfile.CancelCount(), buildpacks.CancelCount())
	}
}

func TestService_CancelBuildBackendFailureDoesNotMarkBuildCanceled(t *testing.T) {
	s, _, _, _, clock := setup(t)
	dockerfile := &testkit.Builder{CancelErr: errors.New("vm control plane unavailable")}
	s.Dockerfile = dockerfile
	requested, err := s.RequestBuild(context.Background(), command("cancel-failure"))
	if err != nil {
		t.Fatal(err)
	}
	build := requested.Build
	persistBuildMutation(t, s.Store, &build, func(value *domain.Build) error {
		return value.Transition(buildv1.BuildFetchingSource, clock.Now())
	})
	persistBuildMutation(t, s.Store, &build, func(value *domain.Build) error {
		return value.Transition(buildv1.BuildDetecting, clock.Now())
	})
	persistBuildMutation(t, s.Store, &build, func(value *domain.Build) error {
		return value.SelectExecution("dockerfile", domain.ExecutionDockerfile, "", clock.Now())
	})

	_, err = s.CancelBuild(context.Background(), "t1", build.ID)
	if !domain.HasCode(err, domain.CodePlatformFailure) {
		t.Fatalf("err=%v", err)
	}
	persisted, _, err := s.GetBuild(context.Background(), "t1", build.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.State != buildv1.BuildBuilding {
		t.Fatalf("backend cancellation failure must preserve state, got %s", persisted.State)
	}
}
