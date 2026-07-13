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

	"github.com/keir-research/ai-native-paas/internal/platformprofile"
	"github.com/keir-research/ai-native-paas/internal/runtime/application"
	"github.com/keir-research/ai-native-paas/internal/runtime/gitops"
	"github.com/keir-research/ai-native-paas/internal/runtime/httpapi"
	"github.com/keir-research/ai-native-paas/internal/runtime/memory"
	buildv1 "github.com/keir-research/ai-native-paas/pkg/contracts/build/v1"
	runtimev1 "github.com/keir-research/ai-native-paas/pkg/contracts/runtime/v1"
)

type environmentArtifactPolicy struct{ allow bool }

func (p environmentArtifactPolicy) IsReleasable(_ context.Context, _ buildv1.ArtifactRef) (buildv1.ReleasabilityDecision, error) {
	if !p.allow {
		return buildv1.ReleasabilityDecision{Allowed: false, PolicyVersion: "development-deny-v1", Reasons: []string{"artifact policy adapter is not configured"}}, nil
	}
	return buildv1.ReleasabilityDecision{Allowed: true, PolicyVersion: "development-allow-v1"}, nil
}

func env(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func main() {
	if _, err := platformprofile.Validate(os.Getenv("PLATFORM_PROFILE"), "runtime-api", platformprofile.Dev("runtime-memory-store"), platformprofile.Dev("local-gitops-repository")); err != nil {
		log.Fatal(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	repository, err := gitops.NewLocalRepository(ctx, env("RUNTIME_GITOPS_ROOT", "/tmp/ai-native-paas-runtime-gitops"))
	if err != nil {
		log.Fatalf("initialize local GitOps repository: %v", err)
	}
	store := memory.New()
	service := application.Service{
		Store:     store,
		Artifacts: environmentArtifactPolicy{allow: strings.EqualFold(env("RUNTIME_DEV_ALLOW_ARTIFACTS", "false"), "true")},
		Renderer:  gitops.Renderer{}, GitOps: repository,
		Units: application.StaticUnitCatalog{"u1": 1, "u2": 2, "u4": 4},
		Clock: application.RealClock{}, IDs: &application.SequentialIDs{}, Scheduler: application.DeterministicScheduler{},
	}
	if _, err := service.RegisterCell(ctx, application.RegisterCellRequest{
		ID: env("RUNTIME_CELL_ID", "cell-local"), Region: env("RUNTIME_REGION", "eu1"),
		Isolation: []runtimev1.IsolationClass{runtimev1.IsolationSandboxed}, CapacityUnits: 1000,
		GitOpsRepository: env("RUNTIME_GITOPS_REPOSITORY", "https://git.example.invalid/platform/runtime-local.git"),
		ClusterServer:    env("RUNTIME_CLUSTER_SERVER", "https://kubernetes.default.svc"),
		ArgoProject:      env("RUNTIME_ARGO_PROJECT", "runtime-cell"), IngressDomain: env("RUNTIME_INGRESS_DOMAIN", "apps.localhost"),
	}); err != nil {
		log.Fatalf("register development runtime cell: %v", err)
	}

	server := &http.Server{
		Addr:              env("RUNTIME_API_ADDR", ":8083"),
		Handler:           httpapi.Handler{Runtime: &service, MaxBodyBytes: 2 << 20},
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second,
	}
	failures := make(chan error, 1)
	go func() {
		log.Printf("runtime-api listening on %s", server.Addr)
		failures <- server.ListenAndServe()
	}()

	select {
	case <-ctx.Done():
	case err := <-failures:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("runtime-api failed: %v", err)
		}
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Printf("runtime-api shutdown: %v", err)
	}
}
