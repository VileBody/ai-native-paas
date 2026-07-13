package acceptance_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/keir-research/ai-native-paas/internal/build/application"
	"github.com/keir-research/ai-native-paas/internal/build/detect"
	"github.com/keir-research/ai-native-paas/internal/build/domain"
	"github.com/keir-research/ai-native-paas/internal/build/localoci"
	"github.com/keir-research/ai-native-paas/internal/build/logs"
	"github.com/keir-research/ai-native-paas/internal/build/memory"
	"github.com/keir-research/ai-native-paas/internal/build/registry"
	"github.com/keir-research/ai-native-paas/internal/build/sbom"
	"github.com/keir-research/ai-native-paas/internal/build/scanner"
	"github.com/keir-research/ai-native-paas/internal/build/signer"
	buildsource "github.com/keir-research/ai-native-paas/internal/build/source"
	"github.com/keir-research/ai-native-paas/internal/build/testkit"
	buildv1 "github.com/keir-research/ai-native-paas/pkg/contracts/build/v1"
	sourcev1 "github.com/keir-research/ai-native-paas/pkg/contracts/source/v1"
)

type acceptanceBuildResolver struct{ access buildsource.RepositoryAccess }

func (r acceptanceBuildResolver) ResolveRepository(_ context.Context, tenantID, repositoryID string) (buildsource.RepositoryAccess, error) {
	if r.access.TenantID != tenantID || r.access.RepositoryID != repositoryID {
		return buildsource.RepositoryAccess{}, domain.NewError(domain.CodeNotFound, "repository not found")
	}
	return r.access, nil
}

func TestAcceptance_SourceRevisionBecomesReleasableArtifact(t *testing.T) {
	for _, binary := range []string{"git", "go"} {
		if _, err := exec.LookPath(binary); err != nil {
			t.Skip(binary + " unavailable")
		}
	}
	remote, commit := buildAcceptanceRepository(t)
	clock := &testkit.Clock{T: time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)}
	ids := &testkit.IDs{}
	store := memory.New()
	logStore := logs.New()
	registryRoot := t.TempDir()
	localRegistry := registry.NewLocal(registryRoot)
	artifactSigner, verifier, err := signer.New("platform-build-key-v1")
	if err != nil {
		t.Fatal(err)
	}
	artifactSigner.Now = clock.Now
	service := &application.Service{
		Store:      store,
		Fetcher:    buildsource.Fetcher{Root: t.TempDir(), Resolver: acceptanceBuildResolver{access: buildsource.RepositoryAccess{TenantID: "tenant-a", RepositoryID: "repo-a", RemoteURL: remote}}},
		Detector:   detect.Detector{},
		Buildpacks: localoci.Builder{Root: t.TempDir()},
		Registry:   localRegistry,
		SBOM:       sbom.Generator{},
		Scanner: scanner.PolicyScanner{
			Provider:       scanner.StaticProvider{},
			MaximumAllowed: scanner.SeverityHigh,
			PolicyVersion:  "policy-2026-07",
			Now:            clock.Now,
		},
		Signer:   artifactSigner,
		Verifier: verifier,
		SecretProvider: testkit.SecretProvider{BuildSecrets: []application.BuildSecret{{
			Name: "private-module-token", Value: "build-secret-must-not-leak", AllowedPhases: []application.BuildPhase{application.PhaseBuild},
		}}, RuntimeSecret: "runtime-secret-must-never-enter-build"},
		Logs:           logStore,
		Clock:          clock,
		IDs:            ids,
		SourceLimits:   application.SourceLimits{MaxBytes: 8 << 20, MaxFiles: 1000},
		RepositoryBase: "registry.test/tenants",
	}
	command := application.RequestBuildCommand{
		TenantID: "tenant-a", ActorID: "agent-a", CorrelationID: "task-42", IdempotencyKey: "build-main-" + commit,
		Source:          sourcev1.SourceRevision{ProjectID: "project-a", RepositoryID: "repo-a", Branch: "main", CommitSHA: commit},
		Config:          domain.BuildConfig{Type: domain.BuildTypeAuto, BuildSecretRef: []string{"private-module-token"}},
		BuilderDigest:   "sha256:" + strings.Repeat("a", 64),
		RunImageDigest:  "sha256:" + strings.Repeat("b", 64),
		PlatformVersion: "build-platform-v1",
	}
	first, err := service.RequestBuild(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.RequestBuild(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	if first.Build.ID != second.Build.ID {
		t.Fatalf("duplicate request created two builds: %s and %s", first.Build.ID, second.Build.ID)
	}
	completed, artifact, err := service.RunBuild(context.Background(), "tenant-a", "agent-a", first.Build.ID)
	if err != nil {
		t.Fatal(err)
	}
	if completed.State != buildv1.BuildSucceeded || artifact == nil || artifact.State != domain.ArtifactReleasable {
		t.Fatalf("build=%s artifact=%+v", completed.State, artifact)
	}
	if err := artifact.Ref().Validate(); err != nil {
		t.Fatalf("invalid frozen ArtifactRef: %v", err)
	}
	resolved, err := localRegistry.Resolve(context.Background(), "tenant-a", artifact.Repository+"@"+artifact.Digest)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Digest != artifact.Digest {
		t.Fatalf("resolved digest=%s want=%s", resolved.Digest, artifact.Digest)
	}
	if artifact.Scan == nil || !artifact.Scan.Passed || artifact.SBOMDigest == "" || artifact.Signature == nil {
		t.Fatalf("trust chain incomplete: %+v", artifact)
	}
	if err := verifier.Verify(context.Background(), artifact.Repository, *artifact.Signature); err != nil {
		t.Fatalf("signature verification: %v", err)
	}
	if mediaType, ok := localRegistry.AttachmentMediaType(artifact.Repository, artifact.SBOMDigest); !ok || mediaType != sbom.MediaType {
		t.Fatalf("SBOM attachment missing: media=%q ok=%v", mediaType, ok)
	}
	decision, err := service.EvaluateArtifact(context.Background(), "tenant-a", artifact.ID)
	if err != nil || !decision.Allowed {
		t.Fatalf("decision=%+v err=%v", decision, err)
	}
	buildLogs, err := service.StreamBuildLogs(context.Background(), "tenant-a", completed.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertNoSecret(t, buildLogs, "build-secret-must-not-leak", "runtime-secret-must-never-enter-build")
	assertTreeHasNoSecret(t, registryRoot, "build-secret-must-not-leak", "runtime-secret-must-never-enter-build")
	builds, artifacts, outbox, audit := store.Snapshot()
	if len(builds) != 1 || len(artifacts) != 1 {
		t.Fatalf("builds=%d artifacts=%d", len(builds), len(artifacts))
	}
	if len(outbox) < 2 || len(audit) < 2 {
		t.Fatalf("outbox=%d audit=%d", len(outbox), len(audit))
	}
}

func TestAcceptance_MonorepoSourceRootBuildsOnlySelectedComponent(t *testing.T) {
	for _, binary := range []string{"git", "go"} {
		if _, err := exec.LookPath(binary); err != nil {
			t.Skip(binary + " unavailable")
		}
	}
	remote, commit := buildAcceptanceRepositoryFiles(t, map[string]string{
		"package.json":     `{"name":"unselected-root"}`,
		"index.js":         "this is intentionally invalid javascript (((\n",
		"apps/api/go.mod":  "module example.test/monorepo-api\n\ngo 1.23\n",
		"apps/api/main.go": "package main\n\nimport \"fmt\"\n\nfunc main() { fmt.Println(\"api\") }\n",
	})
	clock := &testkit.Clock{T: time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)}
	store := memory.New()
	artifactSigner, artifactVerifier, err := signer.New("platform-build-key-v1")
	if err != nil {
		t.Fatal(err)
	}
	artifactSigner.Now = clock.Now
	service := &application.Service{
		Store: store,
		Fetcher: buildsource.Fetcher{Root: t.TempDir(), Resolver: acceptanceBuildResolver{access: buildsource.RepositoryAccess{
			TenantID: "tenant-a", RepositoryID: "repo-monorepo", RemoteURL: remote,
		}}},
		Detector:   detect.Detector{},
		Buildpacks: localoci.Builder{Root: t.TempDir()},
		Registry:   registry.NewLocal(t.TempDir()),
		SBOM:       sbom.Generator{},
		Scanner: scanner.PolicyScanner{
			Provider: scanner.StaticProvider{}, MaximumAllowed: scanner.SeverityHigh,
			PolicyVersion: "policy-2026-07", Now: clock.Now,
		},
		Signer:         artifactSigner,
		Verifier:       artifactVerifier,
		Logs:           logs.New(),
		Clock:          clock,
		IDs:            &testkit.IDs{},
		SourceLimits:   application.SourceLimits{MaxBytes: 8 << 20, MaxFiles: 1000},
		RepositoryBase: "registry.test/tenants",
	}
	requested, err := service.RequestBuild(context.Background(), application.RequestBuildCommand{
		TenantID: "tenant-a", ActorID: "agent-a", CorrelationID: "task-monorepo", IdempotencyKey: "monorepo-" + commit,
		Source:        sourcev1.SourceRevision{ProjectID: "project-monorepo", RepositoryID: "repo-monorepo", Branch: "main", CommitSHA: commit},
		Config:        domain.BuildConfig{Type: domain.BuildTypeAuto, SourceRoot: "apps/api"},
		BuilderDigest: "sha256:" + strings.Repeat("a", 64), RunImageDigest: "sha256:" + strings.Repeat("b", 64), PlatformVersion: "build-platform-v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if requested.Build.Source.SourceRoot != "apps/api" || requested.Build.Config.SourceRoot != "" {
		t.Fatalf("source root was not canonicalized: source=%q config=%q", requested.Build.Source.SourceRoot, requested.Build.Config.SourceRoot)
	}
	completed, artifact, err := service.RunBuild(context.Background(), "tenant-a", "agent-a", requested.Build.ID)
	if err != nil {
		t.Fatal(err)
	}
	if completed.State != buildv1.BuildSucceeded || artifact == nil || artifact.State != domain.ArtifactReleasable {
		t.Fatalf("build=%s artifact=%+v", completed.State, artifact)
	}
}

func buildAcceptanceRepository(t *testing.T) (string, string) {
	t.Helper()
	return buildAcceptanceRepositoryFiles(t, map[string]string{
		"go.mod":  "module example.test/acceptance\n\ngo 1.23\n",
		"main.go": "package main\n\nimport \"fmt\"\n\nfunc main() { fmt.Println(\"acceptance\") }\n",
	})
}

func buildAcceptanceRepositoryFiles(t *testing.T, files map[string]string) (string, string) {
	t.Helper()
	remote := filepath.Join(t.TempDir(), "remote.git")
	runBuildGit(t, "", "git", "init", "--bare", remote)
	work := t.TempDir()
	runBuildGit(t, work, "git", "init")
	runBuildGit(t, work, "git", "config", "user.name", "Build Fixture")
	runBuildGit(t, work, "git", "config", "user.email", "build@example.test")
	for name, content := range files {
		path := filepath.Join(work, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	runBuildGit(t, work, "git", "add", "--all")
	runBuildGit(t, work, "git", "commit", "-m", "exact fixture")
	commit := strings.TrimSpace(runBuildGit(t, work, "git", "rev-parse", "HEAD"))
	runBuildGit(t, work, "git", "branch", "-M", "main")
	runBuildGit(t, work, "git", "remote", "add", "origin", remote)
	runBuildGit(t, work, "git", "push", "origin", "main")
	return remote, commit
}

func runBuildGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command(args[0], args[1:]...)
	if dir == "" {
		dir = os.TempDir()
	}
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull)
	raw, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %v: %s", args, err, raw)
	}
	return string(raw)
}

func assertNoSecret(t *testing.T, raw []byte, secrets ...string) {
	t.Helper()
	for _, secret := range secrets {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("secret leaked: %q", secret)
		}
	}
}

func assertTreeHasNoSecret(t *testing.T, root string, secrets ...string) {
	t.Helper()
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		assertNoSecret(t, raw, secrets...)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
