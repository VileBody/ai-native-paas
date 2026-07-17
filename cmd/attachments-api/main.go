package main

import (
	"context"
	"database/sql"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/keir-research/ai-native-paas/internal/attachments/application"
	"github.com/keir-research/ai-native-paas/internal/attachments/certificate"
	"github.com/keir-research/ai-native-paas/internal/attachments/cozystack"
	"github.com/keir-research/ai-native-paas/internal/attachments/devadapter"
	"github.com/keir-research/ai-native-paas/internal/attachments/dns"
	"github.com/keir-research/ai-native-paas/internal/attachments/domain"
	"github.com/keir-research/ai-native-paas/internal/attachments/httpapi"
	"github.com/keir-research/ai-native-paas/internal/attachments/memory"
	"github.com/keir-research/ai-native-paas/internal/attachments/openbao"
	attachmentspostgres "github.com/keir-research/ai-native-paas/internal/attachments/postgres"
	"github.com/keir-research/ai-native-paas/internal/identity/httpauth"
	"github.com/keir-research/ai-native-paas/internal/identity/oidcverify"
	"github.com/keir-research/ai-native-paas/internal/identity/servicemtls"
	"github.com/keir-research/ai-native-paas/internal/platformprofile"
	"github.com/keir-research/ai-native-paas/internal/postgresbootstrap"
	attachmentsv1 "github.com/keir-research/ai-native-paas/pkg/contracts/attachments/v1"
	commercev1 "github.com/keir-research/ai-native-paas/pkg/contracts/commerce/v1"
)

type postgresEnvironmentDirectory struct{ db *sql.DB }

func (d postgresEnvironmentDirectory) ResolveEnvironment(ctx context.Context, tenant, id string) (application.EnvironmentRef, error) {
	var ref application.EnvironmentRef
	var ready bool
	err := d.db.QueryRowContext(ctx, `SELECT e.tenant_id,e.application_id,e.id,e.name,(a.lifecycle='ACTIVE') FROM runtime.environments e JOIN runtime.applications a ON a.id=e.application_id WHERE e.id=$1 AND e.tenant_id=$2`, id, tenant).Scan(&ref.TenantID, &ref.ApplicationID, &ref.EnvironmentID, &ref.Name, &ready)
	if errors.Is(err, sql.ErrNoRows) {
		return ref, domain.NewError(domain.CodeNotFound, "environment not found")
	}
	if err != nil {
		return ref, domain.Wrap(domain.CodeUnavailable, "environment directory query failed", err)
	}
	ref.Ready = ready
	return ref, nil
}

type denyApprovals struct{}

func (denyApprovals) Verify(context.Context, string, application.ApprovalBinding) error {
	return domain.NewError(domain.CodeUnavailable, "approval service dependency is not installed")
}

type deferredRuntime struct{}

func (deferredRuntime) Publish(context.Context, attachmentsv1.AttachmentSnapshot) error {
	return domain.NewError(domain.CodeUnavailable, "runtime_cell dependency is not installed")
}

type deferredCommerce struct{}

func (deferredCommerce) Check(context.Context, commercev1.EntitlementRequest) (commercev1.EntitlementDecision, error) {
	return commercev1.EntitlementDecision{Allowed: false, Reason: "commerce dependency is not installed", PolicyVersion: "network-deferred-v1"}, nil
}

type deferredUsage struct{}

func (deferredUsage) Append(context.Context, commercev1.UsageEvent) error {
	return domain.NewError(domain.CodeUnavailable, "usage sink dependency is not installed")
}

type structuredLogger struct{}

func (structuredLogger) Log(_ context.Context, event string, fields map[string]string) {
	log.Printf("attachments event=%s field_count=%d", event, len(fields))
}

func env(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func mustPlan(service *application.Service, plan domain.ServicePlan) {
	if err := service.RegisterServicePlan(context.Background(), plan, "system"); err != nil {
		log.Fatalf("register plan %s: %v", plan.ID, err)
	}
}

func main() {
	profile, err := platformprofile.Parse(os.Getenv("PLATFORM_PROFILE"))
	if err != nil {
		log.Fatal(err)
	}
	var adapters []platformprofile.Adapter
	if profile == platformprofile.Production {
		adapters = []platformprofile.Adapter{
			platformprofile.Prod("attachments-postgres-store"),
			platformprofile.Prod("openbao-kv-v2-write-only-secrets"),
			platformprofile.Prod("runtime-postgres-environment-directory"),
			platformprofile.Prod("oidc-jwks-verifier"),
			platformprofile.Prod("postgres-membership-resolver"),
			platformprofile.Prod("verified-identity-middleware"),
			platformprofile.Prod("internal-spiffe-mtls-server"),
			platformprofile.Prod("fail-closed-cozystack-until-runtime-cell"),
			platformprofile.Prod("fail-closed-dns-acme-until-network-gate"),
			platformprofile.Prod("fail-closed-approval-commerce-runtime-gateways"),
		}
	} else {
		adapters = []platformprofile.Adapter{
			platformprofile.Dev("attachments-memory-store"),
			platformprofile.Dev("openbao-memory-backend"),
			platformprofile.Dev("managed-provider-development-gateways"),
			platformprofile.Dev("development-identity-headers"),
		}
	}
	if _, err := platformprofile.Validate(string(profile), "attachments-api", adapters...); err != nil {
		log.Fatal(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	tenantID := env("ATTACHMENTS_DEV_TENANT_ID", "tenant-local")
	applicationID := env("ATTACHMENTS_DEV_APPLICATION_ID", "app-local")
	environmentID := env("ATTACHMENTS_DEV_ENVIRONMENT_ID", "env-local")

	var (
		store         application.Store
		storageName   string
		environments  application.EnvironmentDirectory
		secrets       application.SecretProvider
		provider      application.ManagedServiceProvider
		dnsResolver   application.DNSResolver
		certificates  application.CertificateProvider
		approvals     application.ApprovalVerifier
		runtimeTarget application.RuntimeSnapshotPublisher
		commerce      application.CommercialEntitlementPort
		usage         application.UsageSink
		logger        application.StructuredLogger
		oidcVerifier  httpauth.OIDCVerifier
	)
	if profile == platformprofile.Production {
		db, openErr := postgresbootstrap.Open(ctx, os.Getenv("DATABASE_URL"))
		if openErr != nil {
			log.Fatal(openErr)
		}
		defer db.Close()
		postgresStore, storeErr := attachmentspostgres.NewStore(db)
		if storeErr != nil {
			log.Fatal(storeErr)
		}
		if migrateErr := postgresbootstrap.WithMigrationLock(ctx, db, "attachments", postgresStore.Migrate); migrateErr != nil {
			log.Fatal(migrateErr)
		}
		backend, backendErr := openbao.NewKVV2Backend(openbao.KVV2Config{
			Address: env("OPENBAO_ADDR", ""), TokenFile: env("OPENBAO_TOKEN_FILE", ""), Mount: env("OPENBAO_KV_MOUNT", "attachments"),
		})
		if backendErr != nil {
			log.Fatal(backendErr)
		}
		verifier, verifierErr := oidcverify.NewPostgresVerifier(ctx, db, os.Getenv("OIDC_ISSUER"), os.Getenv("OIDC_CLIENT_ID"))
		if verifierErr != nil {
			log.Fatal(verifierErr)
		}
		store = postgresStore
		storageName = "postgres"
		environments = postgresEnvironmentDirectory{db: db}
		secrets = openbao.Adapter{Backend: backend}
		provider = cozystack.Adapter{}
		dnsResolver = dns.Adapter{}
		certificates = certificate.Adapter{}
		approvals, runtimeTarget, commerce, usage, logger = denyApprovals{}, deferredRuntime{}, deferredCommerce{}, deferredUsage{}, structuredLogger{}
		oidcVerifier = verifier
	} else {
		store = memory.New()
		storageName = "memory-development-only"
		backend := openbao.NewMemoryBackend()
		environments = devadapter.NewEnvironments(application.EnvironmentRef{
			TenantID: tenantID, ApplicationID: applicationID, EnvironmentID: environmentID, Name: "production", Ready: true,
		})
		secrets = openbao.Adapter{Backend: backend}
		provider = devadapter.NewManagedProvider()
		dnsResolver = devadapter.NewDNS()
		certificates = devadapter.Certificates{}
		approvals, runtimeTarget, commerce, usage, logger = devadapter.Approvals{}, devadapter.NewRuntime(), devadapter.Commerce{}, devadapter.Usage{}, devadapter.Logger{}
	}
	service := &application.Service{
		Store:               store,
		Environments:        environments,
		Secrets:             secrets,
		Provider:            provider,
		DNS:                 dnsResolver,
		Certificates:        certificates,
		Approvals:           approvals,
		Runtime:             runtimeTarget,
		Commerce:            commerce,
		Usage:               usage,
		Logger:              logger,
		Clock:               application.RealClock{},
		IDs:                 &application.SequentialIDs{},
		DefaultDomain:       env("ATTACHMENTS_DEFAULT_DOMAIN", "network-deferred.invalid"),
		DNSObservationDelay: time.Second,
		DomainQuarantine:    24 * time.Hour,
	}
	now := time.Now().UTC()
	pg, _ := domain.NewServicePlan("pg-small", 1, attachmentsv1.ServicePostgreSQL, "cozystack", "small", "v1", false, []string{"connect", "read", "write"}, true, true, now)
	redis, _ := domain.NewServicePlan("redis-small", 1, attachmentsv1.ServiceRedis, "cozystack", "small", "v1", false, []string{"connect", "read", "write"}, true, false, now)
	s3, _ := domain.NewServicePlan("s3-small", 1, attachmentsv1.ServiceS3, "cozystack", "small", "v1", false, []string{"read", "write"}, true, false, now)
	mustPlan(service, pg)
	mustPlan(service, redis)
	mustPlan(service, s3)

	baseHandler := http.Handler(httpapi.Handler{Attachments: service, MaxBodyBytes: 1 << 20})
	handler := (httpauth.Middleware{Profile: profile, OIDC: oidcVerifier, PublicPaths: map[string]struct{}{`/healthz`: {}}}).Wrap(baseHandler)
	server := &http.Server{
		Addr:              env("ATTACHMENTS_API_ADDR", ":8084"),
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	var internalServer *http.Server
	if profile == platformprofile.Production {
		internalServer, err = servicemtls.NewServer(servicemtls.ServerConfigFromEnv("agent-api", "attachments:read attachments:write"), baseHandler)
		if err != nil {
			log.Fatal(err)
		}
	}
	failures := make(chan error, 2)
	go func() {
		log.Printf("attachments-api listening on %s (storage=%s profile=%s)", server.Addr, storageName, profile)
		failures <- server.ListenAndServe()
	}()
	if internalServer != nil {
		go func() {
			log.Printf("attachments internal mTLS API listening on %s", internalServer.Addr)
			failures <- internalServer.ListenAndServeTLS("", "")
		}()
	}

	select {
	case <-ctx.Done():
	case err := <-failures:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("attachments-api failed: %v", err)
		}
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Printf("attachments-api shutdown: %v", err)
	}
	if internalServer != nil {
		if err := internalServer.Shutdown(shutdownCtx); err != nil {
			log.Printf("attachments internal mTLS API shutdown: %v", err)
		}
	}
}
