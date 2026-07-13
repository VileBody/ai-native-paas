package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/keir-research/ai-native-paas/internal/platformprofile"
	"github.com/keir-research/ai-native-paas/internal/runtime/application"
	"github.com/keir-research/ai-native-paas/internal/runtime/kubeapi"
	"github.com/keir-research/ai-native-paas/internal/runtime/operator"
	runtimev1 "github.com/keir-research/ai-native-paas/pkg/contracts/runtime/v1"
)

func env(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}
func durationEnv(name string, fallback time.Duration) time.Duration {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}
	parsed, err := time.ParseDuration(value)
	if err != nil || parsed <= 0 {
		log.Fatalf("invalid %s=%q", name, value)
	}
	return parsed
}

func main() {
	if _, err := platformprofile.Validate(os.Getenv("PLATFORM_PROFILE"), "runtime-operator", platformprofile.Prod("kubernetes-api")); err != nil {
		log.Fatal(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	config, err := kubeapi.InClusterConfig()
	if err != nil {
		log.Fatalf("load in-cluster Kubernetes configuration: %v", err)
	}
	client, err := kubeapi.New(config)
	if err != nil {
		log.Fatalf("create Kubernetes client: %v", err)
	}
	reconciler := operator.Reconciler{
		Client: client, Clock: application.RealClock{},
		Units: map[string]runtimev1.ResourceQuantity{
			"u1": {CPU: "250m", Memory: "512Mi", EphemeralStorage: "1Gi"},
			"u2": {CPU: "500m", Memory: "1Gi", EphemeralStorage: "2Gi"},
			"u4": {CPU: "1", Memory: "2Gi", EphemeralStorage: "4Gi"},
		},
	}
	interval := durationEnv("RUNTIME_OPERATOR_POLL_INTERVAL", 5*time.Second)
	health := &http.Server{Addr: env("RUNTIME_OPERATOR_HEALTH_ADDR", ":8081"), Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}), ReadHeaderTimeout: 3 * time.Second}
	go func() {
		if err := health.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("health server failed: %v", err)
			stop()
		}
	}()

	reconcileAll := func() {
		apps, err := client.ListPaaSApps(ctx)
		if err != nil {
			log.Printf("list PaaSApps: %v", err)
			return
		}
		for _, app := range apps {
			reconcileCtx, cancel := context.WithTimeout(ctx, durationEnv("RUNTIME_OPERATOR_RECONCILE_TIMEOUT", 30*time.Second))
			err := reconciler.Reconcile(reconcileCtx, app)
			cancel()
			if err != nil {
				log.Printf("reconcile %s/%s generation=%s: %v", app.Metadata.Namespace, app.Metadata.Name, strconv.FormatInt(app.Metadata.Generation, 10), err)
			}
		}
	}
	reconcileAll()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = health.Shutdown(shutdownCtx)
			cancel()
			return
		case <-ticker.C:
			reconcileAll()
		}
	}
}
