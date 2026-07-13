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
	"github.com/keir-research/ai-native-paas/internal/platformprofile"
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
	if _, err := platformprofile.Validate(os.Getenv("PLATFORM_PROFILE"), "commerce-api", platformprofile.Dev("commerce-memory-store")); err != nil {
		log.Fatal(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	ownership := application.ResourceOwnership(application.DenyAllOwnership{})
	if strings.EqualFold(strings.TrimSpace(os.Getenv("COMMERCE_DEV_ALLOW_ALL_OWNERSHIP")), "true") {
		log.Print("WARNING: COMMERCE_DEV_ALLOW_ALL_OWNERSHIP is enabled; use only for local smoke tests")
		ownership = application.AllowAllOwnership{}
	}
	service := &application.Service{
		Store: memory.New(), Clock: realClock{}, IDs: &sequentialIDs{},
		Ownership: ownership, DriftAlertThreshold: 60,
	}
	server := &http.Server{
		Addr:              env("COMMERCE_API_ADDR", ":8084"),
		Handler:           httpapi.Handler{Commerce: service, MaxBodyBytes: 2 << 20},
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second,
		WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second,
	}
	failures := make(chan error, 1)
	go func() {
		log.Printf("commerce-api listening on %s", server.Addr)
		failures <- server.ListenAndServe()
	}()
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
}
