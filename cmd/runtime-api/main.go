package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/keir-research/ai-native-paas/internal/identity/httpauth"
	"github.com/keir-research/ai-native-paas/internal/identity/oidcverify"
	"github.com/keir-research/ai-native-paas/internal/platformprofile"
	"github.com/keir-research/ai-native-paas/internal/postgresbootstrap"
	"github.com/keir-research/ai-native-paas/internal/runtime/application"
	"github.com/keir-research/ai-native-paas/internal/runtime/domain"
	"github.com/keir-research/ai-native-paas/internal/runtime/gitops"
	"github.com/keir-research/ai-native-paas/internal/runtime/httpapi"
	"github.com/keir-research/ai-native-paas/internal/runtime/memory"
	runtimepostgres "github.com/keir-research/ai-native-paas/internal/runtime/postgres"
	buildv1 "github.com/keir-research/ai-native-paas/pkg/contracts/build/v1"
	runtimev1 "github.com/keir-research/ai-native-paas/pkg/contracts/runtime/v1"
)

type environmentArtifactPolicy struct {
	allow   bool
	version string
	reason  string
}

func (p environmentArtifactPolicy) IsReleasable(_ context.Context, _ buildv1.ArtifactRef) (buildv1.ReleasabilityDecision, error) {
	if !p.allow {
		return buildv1.ReleasabilityDecision{Allowed: false, PolicyVersion: p.version, Reasons: []string{p.reason}}, nil
	}
	return buildv1.ReleasabilityDecision{Allowed: true, PolicyVersion: p.version}, nil
}

// unavailableGitOps prevents any direct runtime mutation while the internal
// GitLab writer and Argo bridge are NETWORK_DEFERRED.
type unavailableGitOps struct{}

func (unavailableGitOps) Commit(context.Context, application.CommitRequest) (application.CommitResult, error) {
	return application.CommitResult{}, domain.NewError(domain.CodeUnavailable, "runtime_cell dependency is not installed")
}
func (unavailableGitOps) FindByRelease(context.Context, string, string) (application.CommitResult, bool, error) {
	return application.CommitResult{}, false, domain.NewError(domain.CodeUnavailable, "runtime_cell dependency is not installed")
}

func env(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func main() {
	profile, err := platformprofile.Parse(os.Getenv("PLATFORM_PROFILE"))
	if err != nil {
		log.Fatal(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	var (
		store        application.Store
		repository   application.GitOpsRepository
		artifacts    buildv1.ArtifactPolicy
		oidcVerifier httpauth.OIDCVerifier
		storageName  string
		adapters     []platformprofile.Adapter
	)
	if profile == platformprofile.Production {
		bootstrapCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
		db, openErr := postgresbootstrap.Open(bootstrapCtx, os.Getenv("DATABASE_URL"))
		if openErr != nil {
			log.Fatal(openErr)
		}
		defer db.Close()
		postgresStore, storeErr := runtimepostgres.NewStore(db)
		if storeErr != nil {
			log.Fatal(storeErr)
		}
		if migrateErr := postgresbootstrap.WithMigrationLock(bootstrapCtx, db, "runtime", postgresStore.Migrate); migrateErr != nil {
			log.Fatal(migrateErr)
		}
		verifier, verifierErr := oidcverify.NewPostgresVerifier(bootstrapCtx, db, os.Getenv("OIDC_ISSUER"), os.Getenv("OIDC_CLIENT_ID"))
		if verifierErr != nil {
			log.Fatal(verifierErr)
		}
		store, repository, oidcVerifier, storageName = postgresStore, unavailableGitOps{}, verifier, "postgres"
		artifacts = environmentArtifactPolicy{version: "network-deferred-v1", reason: "artifact trust and runtime_cell dependencies are not installed"}
		adapters = []platformprofile.Adapter{
			platformprofile.Prod("runtime-postgres-store"),
			platformprofile.Prod("oidc-jwks-verifier"),
			platformprofile.Prod("postgres-membership-resolver"),
			platformprofile.Prod("verified-identity-middleware"),
			platformprofile.Prod("fail-closed-artifact-policy-until-harbor"),
			platformprofile.Prod("fail-closed-gitops-until-runtime-cell"),
		}
	} else {
		localRepository, repoErr := gitops.NewLocalRepository(ctx, env("RUNTIME_GITOPS_ROOT", "/tmp/ai-native-paas-runtime-gitops"))
		if repoErr != nil {
			log.Fatalf("initialize local GitOps repository: %v", repoErr)
		}
		store, repository, storageName = memory.New(), localRepository, "memory-development-only"
		artifacts = environmentArtifactPolicy{allow: strings.EqualFold(env("RUNTIME_DEV_ALLOW_ARTIFACTS", "false"), "true"), version: "development-v1", reason: "development artifact policy is disabled"}
		adapters = []platformprofile.Adapter{
			platformprofile.Dev("runtime-memory-store"),
			platformprofile.Dev("local-gitops-repository"),
			platformprofile.Dev("development-identity-headers"),
		}
	}
	if _, err := platformprofile.Validate(string(profile), "runtime-api", adapters...); err != nil {
		log.Fatal(err)
	}

	service := application.Service{
		Store: store, Artifacts: artifacts, Renderer: gitops.Renderer{}, GitOps: repository,
		Units: application.StaticUnitCatalog{"u1": 1, "u2": 2, "u4": 4},
		Clock: application.RealClock{}, IDs: &application.SequentialIDs{}, Scheduler: application.DeterministicScheduler{},
	}
	if profile != platformprofile.Production {
		if _, err := service.RegisterCell(ctx, application.RegisterCellRequest{
			ID: env("RUNTIME_CELL_ID", "cell-local"), Region: env("RUNTIME_REGION", "eu1"),
			Isolation: []runtimev1.IsolationClass{runtimev1.IsolationSandboxed}, CapacityUnits: 1000,
			GitOpsRepository: env("RUNTIME_GITOPS_REPOSITORY", "https://git.example.invalid/platform/runtime-local.git"),
			ClusterServer:    env("RUNTIME_CLUSTER_SERVER", "https://kubernetes.default.svc"),
			ArgoProject:      env("RUNTIME_ARGO_PROJECT", "runtime-cell"), IngressDomain: env("RUNTIME_INGRESS_DOMAIN", "apps.localhost"),
		}); err != nil {
			log.Fatalf("register development runtime cell: %v", err)
		}
	}

	var handler http.Handler = httpapi.Handler{Runtime: &service, MaxBodyBytes: 2 << 20}
	handler = (httpauth.Middleware{Profile: profile, OIDC: oidcVerifier, PublicPaths: map[string]struct{}{`/healthz`: {}}}).Wrap(handler)
	server := &http.Server{
		Addr: env("RUNTIME_API_ADDR", ":8083"), Handler: handler,
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second,
		WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second,
	}
	go func() {
		log.Printf("runtime-api listening on %s (storage=%s profile=%s)", server.Addr, storageName, profile)
		if serveErr := server.ListenAndServe(); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			log.Printf("runtime-api failed: %v", serveErr)
			stop()
		}
	}()
	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Printf("runtime-api shutdown: %v", err)
	}
}
