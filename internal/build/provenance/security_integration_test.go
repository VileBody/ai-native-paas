package provenance_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/keir-research/ai-native-paas/internal/build/application"
	"github.com/keir-research/ai-native-paas/internal/build/cache"
	"github.com/keir-research/ai-native-paas/internal/build/domain"
	"github.com/keir-research/ai-native-paas/internal/build/localoci"
	"github.com/keir-research/ai-native-paas/internal/build/logs"
	"github.com/keir-research/ai-native-paas/internal/build/provenance"
	"github.com/keir-research/ai-native-paas/internal/build/sbom"
	sourcev1 "github.com/keir-research/ai-native-paas/pkg/contracts/source/v1"
)

func TestBuild_SecretsNeverEnterLayerLogOrProvenance(t *testing.T) {
	const sentinel = "BUILD_SECRET_SENTINEL_7f2e2f4c"
	_, file, _, _ := runtime.Caller(0)
	repositoryRoot := filepath.Clean(filepath.Join(filepath.Dir(file), "../../.."))
	sourcePath := filepath.Join(repositoryRoot, "test", "fixtures", "build", "hello-go")
	revision := sourcev1.SourceRevision{ProjectID: "p1", RepositoryID: "r1", Branch: "main", CommitSHA: strings.Repeat("a", 40)}
	logStore := logs.New()
	logWriter := logStore.Writer("build-secret-test", []string{sentinel})
	output, err := (localoci.Builder{Root: t.TempDir()}).Build(context.Background(), application.BuildExecutionRequest{
		BuildID: "build-secret-test", TenantID: "t1",
		Source:    application.SourceSnapshot{Revision: revision, Path: sourcePath},
		Detection: application.Detection{Runtime: "go", Backend: application.BackendBuildpacks, BuildpackID: "paketo/go"},
		Secrets:   []application.BuildSecret{{Name: "dependency-token", Value: sentinel, AllowedPhases: []application.BuildPhase{application.PhaseBuild}}},
		LogWriter: logWriter, Repository: "registry.test/tenants/t1/apps/p1",
	})
	if err != nil {
		t.Fatal(err)
	}
	artifact := application.PublishedArtifact{Repository: "registry.test/tenants/t1/apps/p1", Digest: output.ManifestDigest, MediaType: output.MediaType}
	sbomResult, err := (sbom.Generator{}).Generate(context.Background(), application.SourceSnapshot{Revision: revision, Path: sourcePath}, artifact)
	if err != nil {
		t.Fatal(err)
	}
	attestor, verifier, err := provenance.New("platform-build-attestor")
	if err != nil {
		t.Fatal(err)
	}
	started := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	provenanceResult, err := attestor.Attest(context.Background(), provenance.Materials{
		BuildID: "build-secret-test", Repository: artifact.Repository, SourceSHA: revision.CommitSHA,
		BuildSpecDigest: "sha256:" + strings.Repeat("b", 64), BuilderDigest: "sha256:" + strings.Repeat("c", 64), OutputDigest: artifact.Digest,
		StartedAt: started, FinishedAt: started.Add(time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := verifier.Verify(context.Background(), provenanceResult.Document); err != nil {
		t.Fatal(err)
	}

	buildCache := cache.New()
	secretPayload := []byte("layer=" + sentinel)
	if err := buildCache.PutWithSecrets(cache.Entry{TenantID: "t1", ProjectID: "p1", Key: "deps", Scope: cache.ScopeProject, Digest: digestForTest(secretPayload), Payload: secretPayload, Metadata: map[string]string{"source": "dependency-fetch"}}, []string{sentinel}); !domain.HasCode(err, domain.CodeInvalidArgument) {
		t.Fatalf("secret-bearing cache entry err=%v", err)
	}
	if _, found, err := buildCache.Get("t1", "p1", "deps", cache.ScopeProject); err != nil || found {
		t.Fatalf("secret-bearing cache entry persisted: found=%v err=%v", found, err)
	}

	logsRaw, err := logStore.Read(context.Background(), "build-secret-test")
	if err != nil {
		t.Fatal(err)
	}
	metadataRaw, _ := json.Marshal(output.Metadata)
	corpus := [][]byte{logsRaw, metadataRaw, sbomResult.Document, provenanceResult.Document}
	err = filepath.WalkDir(output.OCILayoutPath, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		raw, readErr := os.ReadFile(path)
		if readErr == nil {
			corpus = append(corpus, raw)
		}
		return readErr
	})
	if err != nil {
		t.Fatal(err)
	}
	for index, raw := range corpus {
		if strings.Contains(string(raw), sentinel) {
			t.Fatalf("secret sentinel leaked into output corpus item %d", index)
		}
	}
}

func digestForTest(raw []byte) string {
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}
