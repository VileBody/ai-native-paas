package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/keir-research/ai-native-paas/internal/agent/application"
	"github.com/keir-research/ai-native-paas/internal/agent/devadapter"
	"github.com/keir-research/ai-native-paas/internal/agent/httpapi"
	"github.com/keir-research/ai-native-paas/internal/agent/memory"
	agentpostgres "github.com/keir-research/ai-native-paas/internal/agent/postgres"
	"github.com/keir-research/ai-native-paas/internal/agent/support"
	"github.com/keir-research/ai-native-paas/internal/platformprofile"
	"github.com/keir-research/ai-native-paas/internal/postgresbootstrap"
	agentv1 "github.com/keir-research/ai-native-paas/pkg/contracts/agent/v1"
	commercev1 "github.com/keir-research/ai-native-paas/pkg/contracts/commerce/v1"
)

func main() {
	profile, err := platformprofile.Parse(os.Getenv("PLATFORM_PROFILE"))
	if err != nil {
		log.Fatal(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	adapters := []platformprofile.Adapter{
		platformprofile.Dev("source-build-runtime-development-gateways"),
		platformprofile.Dev("development-identity-headers"),
	}
	if profile == platformprofile.Production {
		adapters = append(adapters, platformprofile.Prod("agent-postgres-store"))
	} else {
		adapters = append(adapters, platformprofile.Dev("agent-memory-store"))
	}
	if _, err := platformprofile.Validate(string(profile), "agent-api", adapters...); err != nil {
		log.Fatal(err)
	}

	var (
		store       application.Store
		storageName string
	)
	if profile == platformprofile.Production {
		db, openErr := postgresbootstrap.Open(ctx, os.Getenv("DATABASE_URL"))
		if openErr != nil {
			log.Fatal(openErr)
		}
		defer db.Close()
		postgresStore, storeErr := agentpostgres.NewStore(db)
		if storeErr != nil {
			log.Fatal(storeErr)
		}
		if migrateErr := postgresbootstrap.WithMigrationLock(ctx, db, "agent", postgresStore.Migrate); migrateErr != nil {
			log.Fatal(migrateErr)
		}
		store = postgresStore
		storageName = "postgres"
	} else {
		store = memory.New()
		storageName = "memory-development-only"
	}

	clock := support.Clock{}
	ids := &support.IDs{}
	commerce := &devadapter.Commerce{Allowed: true}
	service := &application.Service{
		Store:       store,
		Clock:       clock,
		IDs:         ids,
		Source:      devadapter.NewSource(),
		Builds:      devadapter.NewBuilds(),
		Runtime:     devadapter.NewRuntime(),
		Attachments: devadapter.NewAttachments(),
		Commerce:    commerce,
		Operations:  devadapter.NewOperations(),
		Logs:        &devadapter.Logs{Lines: []string{"application ready", "token=development-secret"}},
		Usage:       &devadapter.Usage{Preview: commercev1.InvoicePreview{TenantID: env("AGENT_BOOTSTRAP_TENANT", "tenant-dev"), PeriodID: "period-dev", Currency: "EUR"}},
	}

	tenant := env("AGENT_BOOTSTRAP_TENANT", "tenant-dev")
	agent := env("AGENT_BOOTSTRAP_AGENT", "agent-dev")
	user := env("AGENT_BOOTSTRAP_USER", "user-dev")
	task := env("AGENT_BOOTSTRAP_TASK", "task-dev")
	scopes := make([]string, 0, len(agentv1.ToolCatalog()))
	for _, tool := range agentv1.ToolCatalog() {
		scopes = append(scopes, string(agentv1.ScopeForTool(tool)))
	}
	if _, err := service.RegisterPrincipal(ctx, application.RegisterPrincipalCommand{ID: agent, TenantID: tenant, OnBehalfOfUserID: user, Scopes: scopes, CredentialExpiresAt: clock.Now().Add(24 * time.Hour)}); err != nil {
		log.Fatalf("bootstrap agent principal: %v", err)
	}
	if _, err := service.StartTask(ctx, application.StartTaskCommand{ID: task, TenantID: tenant, AgentID: agent, OnBehalfOfUserID: user, CorrelationID: "corr-dev", BudgetPolicy: agentv1.BudgetPolicy{MaxBuildCount: 100, MaxBuildMinutes: 10000, MaxDeployCount: 100, RepairThreshold: 3}}); err != nil {
		log.Fatalf("bootstrap agent task: %v", err)
	}

	server := &http.Server{
		Addr:              env("AGENT_API_ADDR", ":8087"),
		Handler:           httpapi.Handler{Agent: service, MaxBodyBytes: 1 << 20},
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	go func() {
		log.Printf("agent API listening on %s (storage=%s profile=%s; development gateways)", server.Addr, storageName, profile)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("agent API stopped unexpectedly: %v", err)
			stop()
		}
	}()
	<-ctx.Done()
	shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdown); err != nil {
		log.Printf("agent API shutdown: %v", err)
	}
}

func env(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
