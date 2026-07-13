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
	"github.com/keir-research/ai-native-paas/internal/agent/support"
	agentv1 "github.com/keir-research/ai-native-paas/pkg/contracts/agent/v1"
	commercev1 "github.com/keir-research/ai-native-paas/pkg/contracts/commerce/v1"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	clock := support.Clock{}
	ids := &support.IDs{}
	commerce := &devadapter.Commerce{Allowed: true}
	service := &application.Service{
		Store:       memory.New(),
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
		log.Printf("agent API listening on %s (development adapters; replace before production)", server.Addr)
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
