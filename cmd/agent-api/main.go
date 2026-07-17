package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/keir-research/ai-native-paas/internal/agent/application"
	"github.com/keir-research/ai-native-paas/internal/agent/devadapter"
	"github.com/keir-research/ai-native-paas/internal/agent/httpapi"
	"github.com/keir-research/ai-native-paas/internal/agent/memory"
	agentpostgres "github.com/keir-research/ai-native-paas/internal/agent/postgres"
	"github.com/keir-research/ai-native-paas/internal/agent/productiongate"
	"github.com/keir-research/ai-native-paas/internal/agent/support"
	"github.com/keir-research/ai-native-paas/internal/identity/httpauth"
	"github.com/keir-research/ai-native-paas/internal/identity/oidcverify"
	"github.com/keir-research/ai-native-paas/internal/identity/servicemtls"
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

	var adapters []platformprofile.Adapter
	if profile == platformprofile.Production {
		adapters = []platformprofile.Adapter{
			platformprofile.Prod("agent-postgres-store"),
			platformprofile.Prod("oidc-jwks-verifier"),
			platformprofile.Prod("postgres-membership-resolver"),
			platformprofile.Prod("verified-identity-middleware"),
			platformprofile.Prod("commerce-http-gateway-or-fail-closed"),
			platformprofile.Prod("kernel-http-operation-gateway-or-fail-closed"),
			platformprofile.Prod("build-http-gateway-or-fail-closed"),
			platformprofile.Prod("internal-spiffe-mtls-client"),
			platformprofile.Prod("fail-closed-mcp-v1-until-agent-mtls"),
			platformprofile.Prod("fail-closed-service-gateways-until-internal-mtls"),
		}
	} else {
		adapters = []platformprofile.Adapter{
			platformprofile.Dev("agent-memory-store"),
			platformprofile.Dev("source-build-runtime-development-gateways"),
			platformprofile.Dev("development-identity-headers"),
		}
	}
	if _, err := platformprofile.Validate(string(profile), "agent-api", adapters...); err != nil {
		log.Fatal(err)
	}

	var (
		store        application.Store
		storageName  string
		oidcVerifier httpauth.OIDCVerifier
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
		verifier, verifierErr := oidcverify.NewPostgresVerifier(ctx, db, os.Getenv("OIDC_ISSUER"), os.Getenv("OIDC_CLIENT_ID"))
		if verifierErr != nil {
			log.Fatal(verifierErr)
		}
		oidcVerifier = verifier
	} else {
		store = memory.New()
		storageName = "memory-development-only"
	}

	clock := support.Clock{}
	ids := &support.IDs{}
	var (
		sourceGateway     application.SourceGateway
		buildGateway      application.BuildGateway
		runtimeGateway    application.RuntimeGateway
		attachmentGateway application.AttachmentGateway
		commerceGateway   application.EntitlementGateway
		operationGateway  application.OperationGateway
		logGateway        application.LogGateway
		usageGateway      application.UsageGateway
	)
	if profile == platformprofile.Production {
		sourceGateway, runtimeGateway = productiongate.Source{}, productiongate.Runtime{}
		serviceURLs := []string{os.Getenv("COMMERCE_API_URL"), os.Getenv("KERNEL_API_URL"), os.Getenv("BUILD_API_URL"), os.Getenv("RUNTIME_API_URL"), os.Getenv("ATTACHMENTS_API_URL")}
		var internalClient *http.Client
		if anyConfigured(serviceURLs...) {
			if urlErr := requireHTTPS(serviceURLs...); urlErr != nil {
				log.Fatal(urlErr)
			}
			internalClient, err = servicemtls.NewClient(os.Getenv("INTERNAL_MTLS_CA_FILE"), os.Getenv("INTERNAL_MTLS_CLIENT_CERT_FILE"), os.Getenv("INTERNAL_MTLS_CLIENT_KEY_FILE"))
			if err != nil {
				log.Fatal(err)
			}
		}
		principal := env("AGENT_API_SERVICE_PRINCIPAL", "agent-api")
		commerceClient := productiongate.NewHTTPCommerce(os.Getenv("COMMERCE_API_URL"), principal)
		commerceClient.HTTPClient = internalClient
		buildClient := productiongate.NewHTTPBuilds(
			os.Getenv("BUILD_API_URL"), principal,
			os.Getenv("AGENT_BUILD_BUILDER_DIGEST"), os.Getenv("AGENT_BUILD_RUN_IMAGE_DIGEST"), os.Getenv("AGENT_BUILD_PLATFORM_VERSION"),
		)
		buildClient.HTTPClient = internalClient
		buildGateway = buildClient
		runtimeClient := productiongate.NewHTTPRuntime(os.Getenv("RUNTIME_API_URL"), principal)
		runtimeClient.HTTPClient = internalClient
		runtimeGateway = runtimeClient
		attachmentsClient := productiongate.NewHTTPAttachments(os.Getenv("ATTACHMENTS_API_URL"), principal)
		attachmentsClient.HTTPClient = internalClient
		attachmentGateway, commerceGateway = attachmentsClient, commerceClient
		operationsClient := productiongate.NewHTTPOperations(os.Getenv("KERNEL_API_URL"), principal)
		operationsClient.HTTPClient = internalClient
		operationGateway = operationsClient
		logGateway, usageGateway = productiongate.Logs{}, commerceClient
	} else {
		commerce := &devadapter.Commerce{Allowed: true}
		sourceGateway, buildGateway, runtimeGateway = devadapter.NewSource(), devadapter.NewBuilds(), devadapter.NewRuntime()
		attachmentGateway, commerceGateway = devadapter.NewAttachments(), commerce
		operationGateway = devadapter.NewOperations()
		logGateway = &devadapter.Logs{Lines: []string{"application ready", "token=development-secret"}}
		usageGateway = &devadapter.Usage{Preview: commercev1.InvoicePreview{TenantID: env("AGENT_BOOTSTRAP_TENANT", "tenant-dev"), PeriodID: "period-dev", Currency: "EUR"}}
	}
	service := &application.Service{
		Store:       store,
		Clock:       clock,
		IDs:         ids,
		Source:      sourceGateway,
		Builds:      buildGateway,
		Runtime:     runtimeGateway,
		Attachments: attachmentGateway,
		Commerce:    commerceGateway,
		Operations:  operationGateway,
		Logs:        logGateway,
		Usage:       usageGateway,
	}

	if profile != platformprofile.Production {
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
	}

	var handler http.Handler = httpapi.Handler{Agent: service, MaxBodyBytes: 1 << 20}
	handler = (httpauth.Middleware{Profile: profile, OIDC: oidcVerifier, PublicPaths: map[string]struct{}{`/healthz`: {}}}).Wrap(handler)
	server := &http.Server{
		Addr:              env("AGENT_API_ADDR", ":8087"),
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	go func() {
		log.Printf("agent API listening on %s (storage=%s profile=%s)", server.Addr, storageName, profile)
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

func anyConfigured(values ...string) bool {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return true
		}
	}
	return false
}

func requireHTTPS(values ...string) error {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		parsed, err := url.Parse(value)
		if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
			return errors.New("configured internal service URLs must use https")
		}
	}
	return nil
}
