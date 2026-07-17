package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/keir-research/ai-native-paas/internal/commerce/application"
	"github.com/keir-research/ai-native-paas/internal/commerce/httpapi"
	"github.com/keir-research/ai-native-paas/internal/commerce/memory"
	commercepostgres "github.com/keir-research/ai-native-paas/internal/commerce/postgres"
	"github.com/keir-research/ai-native-paas/internal/identity/httpauth"
	"github.com/keir-research/ai-native-paas/internal/identity/oidcverify"
	"github.com/keir-research/ai-native-paas/internal/identity/servicemtls"
	"github.com/keir-research/ai-native-paas/internal/platformprofile"
	"github.com/keir-research/ai-native-paas/internal/postgresbootstrap"
)

type realClock struct{}

func (realClock) Now() time.Time { return time.Now().UTC() }

type sequentialIDs struct {
	mu sync.Mutex
	n  uint64
}

func (i *sequentialIDs) NewID(prefix string) string {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.n++
	return fmt.Sprintf("%s-%08d", prefix, i.n)
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
		storageName  string
		adapters     []platformprofile.Adapter
		oidcVerifier httpauth.OIDCVerifier
	)
	if profile == platformprofile.Production {
		bootstrapCtx, cancelBootstrap := context.WithTimeout(ctx, 2*time.Minute)
		defer cancelBootstrap()
		db, openErr := postgresbootstrap.Open(bootstrapCtx, os.Getenv("DATABASE_URL"))
		if openErr != nil {
			log.Fatal(openErr)
		}
		defer db.Close()
		postgresStore, storeErr := commercepostgres.NewStore(db)
		if storeErr != nil {
			log.Fatal(storeErr)
		}
		if migrateErr := postgresbootstrap.WithMigrationLock(bootstrapCtx, db, "commerce", postgresStore.Migrate); migrateErr != nil {
			log.Fatal(migrateErr)
		}
		store = postgresStore
		verifier, verifierErr := oidcverify.NewPostgresVerifier(bootstrapCtx, db, os.Getenv("OIDC_ISSUER"), os.Getenv("OIDC_CLIENT_ID"))
		if verifierErr != nil {
			log.Fatal(verifierErr)
		}
		oidcVerifier = verifier
		cancelBootstrap()
		storageName = "postgres"
		adapters = []platformprofile.Adapter{
			platformprofile.Prod("commerce-postgres-store"),
			platformprofile.Prod("oidc-jwks-verifier"),
			platformprofile.Prod("postgres-membership-resolver"),
			platformprofile.Prod("verified-identity-middleware"),
			platformprofile.Prod("internal-spiffe-mtls-server"),
			platformprofile.Prod("deny-by-default-ownership"),
		}
	} else {
		store = memory.New()
		storageName = "memory-development-only"
		adapters = []platformprofile.Adapter{platformprofile.Dev("commerce-memory-store"), platformprofile.Dev("development-identity-headers"), platformprofile.Prod("deny-by-default-ownership")}
	}
	ownership := application.ResourceOwnership(application.DenyAllOwnership{})
	if strings.EqualFold(strings.TrimSpace(os.Getenv("COMMERCE_DEV_ALLOW_ALL_OWNERSHIP")), "true") {
		if profile == platformprofile.Production {
			log.Fatal("COMMERCE_DEV_ALLOW_ALL_OWNERSHIP is forbidden in production")
		}
		log.Print("WARNING: COMMERCE_DEV_ALLOW_ALL_OWNERSHIP is enabled; use only for local smoke tests")
		ownership = application.AllowAllOwnership{}
		adapters = append(adapters, platformprofile.Dev("allow-all-ownership"))
	}
	if _, err := platformprofile.Validate(string(profile), "commerce-api", adapters...); err != nil {
		log.Fatal(err)
	}
	service := &application.Service{
		Store: store, Clock: realClock{}, IDs: &sequentialIDs{},
		Ownership: ownership, DriftAlertThreshold: 60,
	}
	baseHandler := http.Handler(httpapi.Handler{Commerce: service, MaxBodyBytes: 2 << 20})
	handler := (httpauth.Middleware{Profile: profile, OIDC: oidcVerifier, PublicPaths: map[string]struct{}{`/healthz`: {}}}).Wrap(baseHandler)
	server := &http.Server{
		Addr:              env("COMMERCE_API_ADDR", ":8084"),
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second,
		WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second,
	}
	var internalServer *http.Server
	if profile == platformprofile.Production {
		internalServer, err = servicemtls.NewServer(servicemtls.ServerConfigFromEnv("agent-api,workspace-manager,attachments-api", "commerce:read commerce:write"), baseHandler)
		if err != nil {
			log.Fatal(err)
		}
	}
	failures := make(chan error, 2)
	go func() {
		log.Printf("commerce-api listening on %s (storage=%s profile=%s)", server.Addr, storageName, profile)
		failures <- server.ListenAndServe()
	}()
	if internalServer != nil {
		go func() {
			log.Printf("commerce internal mTLS API listening on %s", internalServer.Addr)
			failures <- internalServer.ListenAndServeTLS("", "")
		}()
	}
	select {
	case <-ctx.Done():
	case err := <-failures:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("commerce-api failed: %v", err)
		}
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Printf("commerce-api shutdown: %v", err)
	}
	if internalServer != nil {
		if err := internalServer.Shutdown(shutdownCtx); err != nil {
			log.Printf("commerce internal mTLS API shutdown: %v", err)
		}
	}
}
