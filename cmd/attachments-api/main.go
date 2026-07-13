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

	"github.com/keir-research/ai-native-paas/internal/attachments/application"
	"github.com/keir-research/ai-native-paas/internal/attachments/devadapter"
	"github.com/keir-research/ai-native-paas/internal/attachments/domain"
	"github.com/keir-research/ai-native-paas/internal/attachments/httpapi"
	"github.com/keir-research/ai-native-paas/internal/attachments/memory"
	"github.com/keir-research/ai-native-paas/internal/attachments/openbao"
	attachmentsv1 "github.com/keir-research/ai-native-paas/pkg/contracts/attachments/v1"
)

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
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	tenantID := env("ATTACHMENTS_DEV_TENANT_ID", "tenant-local")
	applicationID := env("ATTACHMENTS_DEV_APPLICATION_ID", "app-local")
	environmentID := env("ATTACHMENTS_DEV_ENVIRONMENT_ID", "env-local")

	store := memory.New()
	backend := openbao.NewMemoryBackend()
	service := &application.Service{
		Store: store,
		Environments: devadapter.NewEnvironments(application.EnvironmentRef{
			TenantID: tenantID, ApplicationID: applicationID, EnvironmentID: environmentID, Name: "production", Ready: true,
		}),
		Secrets:             openbao.Adapter{Backend: backend},
		Provider:            devadapter.NewManagedProvider(),
		DNS:                 devadapter.NewDNS(),
		Certificates:        devadapter.Certificates{},
		Approvals:           devadapter.Approvals{},
		Runtime:             devadapter.NewRuntime(),
		Commerce:            devadapter.Commerce{},
		Usage:               devadapter.Usage{},
		Logger:              devadapter.Logger{},
		Clock:               application.RealClock{},
		IDs:                 &application.SequentialIDs{},
		DefaultDomain:       env("ATTACHMENTS_DEFAULT_DOMAIN", "apps.localhost"),
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

	server := &http.Server{
		Addr:              env("ATTACHMENTS_API_ADDR", ":8084"),
		Handler:           httpapi.Handler{Attachments: service, MaxBodyBytes: 1 << 20},
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	failures := make(chan error, 1)
	go func() {
		log.Printf("attachments-api listening on %s", server.Addr)
		failures <- server.ListenAndServe()
	}()

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
}
