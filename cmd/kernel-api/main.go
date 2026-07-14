package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	kernelpostgres "github.com/keir-research/ai-native-paas/adapters/postgres/kernel"
	"github.com/keir-research/ai-native-paas/internal/identity/httpauth"
	"github.com/keir-research/ai-native-paas/internal/identity/oidcverify"
	"github.com/keir-research/ai-native-paas/internal/kernel"
	"github.com/keir-research/ai-native-paas/internal/kernel/httpapi"
	"github.com/keir-research/ai-native-paas/internal/kernel/memory"
	"github.com/keir-research/ai-native-paas/internal/platformprofile"
	"github.com/keir-research/ai-native-paas/internal/postgresbootstrap"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	profile, err := platformprofile.Parse(os.Getenv("PLATFORM_PROFILE"))
	if err != nil {
		logger.Error("invalid runtime profile", "error", err)
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	var (
		store        kernel.Store
		storageName  string
		adapters     []platformprofile.Adapter
		oidcVerifier httpauth.OIDCVerifier
	)
	if profile == platformprofile.Production {
		bootstrapCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
		db, openErr := postgresbootstrap.Open(bootstrapCtx, os.Getenv("DATABASE_URL"))
		if openErr != nil {
			logger.Error("initialize PostgreSQL", "error", openErr)
			os.Exit(1)
		}
		defer db.Close()
		if migrateErr := postgresbootstrap.WithMigrationLock(bootstrapCtx, db, "kernel", func(migrateCtx context.Context) error {
			return kernelpostgres.Migrate(migrateCtx, db)
		}); migrateErr != nil {
			logger.Error("migrate PostgreSQL", "error", migrateErr)
			os.Exit(1)
		}
		store, err = kernelpostgres.NewStore(db)
		if err != nil {
			logger.Error("initialize kernel store", "error", err)
			os.Exit(1)
		}
		verifier, verifierErr := oidcverify.NewPostgresVerifier(bootstrapCtx, db, os.Getenv("OIDC_ISSUER"), os.Getenv("OIDC_CLIENT_ID"))
		if verifierErr != nil {
			logger.Error("initialize OIDC identity", "error", verifierErr)
			os.Exit(1)
		}
		oidcVerifier = verifier
		storageName = "postgres"
		adapters = []platformprofile.Adapter{
			platformprofile.Prod("kernel-postgres-store"),
			platformprofile.Prod("oidc-jwks-verifier"),
			platformprofile.Prod("postgres-membership-resolver"),
			platformprofile.Prod("verified-identity-middleware"),
		}
	} else {
		store = memory.NewStore()
		storageName = "memory-development-only"
		adapters = []platformprofile.Adapter{platformprofile.Dev("kernel-memory-store"), platformprofile.Dev("development-identity-headers")}
	}
	if err != nil {
		logger.Error("initialize kernel store", "error", err)
		os.Exit(1)
	}
	if _, err := platformprofile.Validate(string(profile), "kernel-api", adapters...); err != nil {
		logger.Error("invalid runtime adapters", "error", err)
		os.Exit(1)
	}
	ids := kernel.CryptoIDGenerator{}
	service, err := kernel.NewService(store, kernel.SystemClock{}, ids)
	if err != nil {
		logger.Error("initialize kernel service", "error", err)
		os.Exit(1)
	}
	handler, err := httpapi.NewHandler(service, ids)
	if err != nil {
		logger.Error("initialize HTTP handler", "error", err)
		os.Exit(1)
	}

	address := os.Getenv("KERNEL_HTTP_ADDR")
	if address == "" {
		address = ":8080"
	}
	handler = (httpauth.Middleware{
		Profile: profile,
		OIDC:    oidcVerifier,
		PublicPaths: map[string]struct{}{
			"/healthz": {},
		},
	}).Wrap(handler)
	server := &http.Server{
		Addr:              address,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			logger.Error("HTTP shutdown", "error", err)
		}
	}()

	logger.Info("kernel API listening", "address", address, "storage", storageName, "profile", profile)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Error("HTTP server failed", "error", err)
		os.Exit(1)
	}
}
