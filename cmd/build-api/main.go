package main

import (
	"context"
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/keir-research/ai-native-paas/internal/build/application"
	builddomain "github.com/keir-research/ai-native-paas/internal/build/domain"
	"github.com/keir-research/ai-native-paas/internal/build/httpapi"
	"github.com/keir-research/ai-native-paas/internal/build/logs"
	"github.com/keir-research/ai-native-paas/internal/build/memory"
	buildpostgres "github.com/keir-research/ai-native-paas/internal/build/postgres"
	"github.com/keir-research/ai-native-paas/internal/build/support"
	"github.com/keir-research/ai-native-paas/internal/identity/httpauth"
	"github.com/keir-research/ai-native-paas/internal/identity/oidcverify"
	"github.com/keir-research/ai-native-paas/internal/identity/servicemtls"
	"github.com/keir-research/ai-native-paas/internal/platformprofile"
	"github.com/keir-research/ai-native-paas/internal/postgresbootstrap"
)

// unavailableLogs is deliberate fail-closed production wiring until the
// Harbor/S3 log path is installed in Phase C. It never buffers secrets in RAM.
type unavailableLogs struct{}

func (unavailableLogs) Writer(string, []string) io.Writer { return io.Discard }
func (unavailableLogs) Read(context.Context, string) ([]byte, error) {
	return nil, builddomain.NewError(builddomain.CodeUnavailable, "production build log store is not installed")
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
		logStore     application.LogStore
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
		postgresStore, storeErr := buildpostgres.NewStore(db)
		if storeErr != nil {
			log.Fatal(storeErr)
		}
		if migrateErr := postgresbootstrap.WithMigrationLock(bootstrapCtx, db, "build", postgresStore.Migrate); migrateErr != nil {
			log.Fatal(migrateErr)
		}
		verifier, verifierErr := oidcverify.NewPostgresVerifier(bootstrapCtx, db, os.Getenv("OIDC_ISSUER"), os.Getenv("OIDC_CLIENT_ID"))
		if verifierErr != nil {
			log.Fatal(verifierErr)
		}
		store, logStore, oidcVerifier, storageName = postgresStore, unavailableLogs{}, verifier, "postgres"
		adapters = []platformprofile.Adapter{
			platformprofile.Prod("build-postgres-store"),
			platformprofile.Prod("oidc-jwks-verifier"),
			platformprofile.Prod("postgres-membership-resolver"),
			platformprofile.Prod("verified-identity-middleware"),
			platformprofile.Prod("internal-spiffe-mtls-server"),
			platformprofile.Prod("fail-closed-build-execution-until-workspace-image"),
			platformprofile.Prod("fail-closed-build-logs-until-harbor-s3"),
		}
	} else {
		store, logStore, storageName = memory.New(), logs.New(), "memory-development-only"
		adapters = []platformprofile.Adapter{
			platformprofile.Dev("build-memory-store"),
			platformprofile.Dev("build-memory-logs"),
			platformprofile.Dev("development-identity-headers"),
		}
	}
	if _, err := platformprofile.Validate(string(profile), "build-api", adapters...); err != nil {
		log.Fatal(err)
	}

	service := &application.Service{
		Store: store, Logs: logStore, Clock: support.Clock{}, IDs: &support.IDs{},
		RepositoryBase: os.Getenv("BUILD_REGISTRY_BASE"),
	}
	baseHandler := http.Handler(httpapi.Handler{Build: service, MaxBodyBytes: 2 << 20})
	handler := (httpauth.Middleware{Profile: profile, OIDC: oidcVerifier, PublicPaths: map[string]struct{}{`/healthz`: {}}}).Wrap(baseHandler)
	server := &http.Server{
		Addr: env("LISTEN_ADDR", ":8082"), Handler: handler,
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second,
		WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second,
	}
	var internalServer *http.Server
	if profile == platformprofile.Production {
		internalServer, err = servicemtls.NewServer(servicemtls.ServerConfigFromEnv("agent-api", "build:read build:write"), baseHandler)
		if err != nil {
			log.Fatal(err)
		}
	}
	go func() {
		log.Printf("build api listening on %s (storage=%s profile=%s)", server.Addr, storageName, profile)
		if serveErr := server.ListenAndServe(); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			log.Printf("build api failed: %v", serveErr)
			stop()
		}
	}()
	if internalServer != nil {
		go func() {
			log.Printf("build internal mTLS API listening on %s", internalServer.Addr)
			if serveErr := internalServer.ListenAndServeTLS("", ""); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
				log.Printf("build internal mTLS API failed: %v", serveErr)
				stop()
			}
		}()
	}
	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Printf("build api shutdown: %v", err)
	}
	if internalServer != nil {
		if err := internalServer.Shutdown(shutdownCtx); err != nil {
			log.Printf("build internal mTLS API shutdown: %v", err)
		}
	}
}
