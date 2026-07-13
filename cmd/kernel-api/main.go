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

	"github.com/keir-research/ai-native-paas/internal/kernel"
	"github.com/keir-research/ai-native-paas/internal/kernel/httpapi"
	"github.com/keir-research/ai-native-paas/internal/kernel/memory"
	"github.com/keir-research/ai-native-paas/internal/platformprofile"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if _, err := platformprofile.Validate(os.Getenv("PLATFORM_PROFILE"), "kernel-api", platformprofile.Dev("kernel-memory-store"), platformprofile.Dev("development-identity-headers")); err != nil {
		logger.Error("invalid runtime profile", "error", err)
		os.Exit(1)
	}
	ids := kernel.CryptoIDGenerator{}
	service, err := kernel.NewService(memory.NewStore(), kernel.SystemClock{}, ids)
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
	server := &http.Server{
		Addr:              address,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			logger.Error("HTTP shutdown", "error", err)
		}
	}()

	logger.Info("kernel API listening", "address", address, "storage", "memory-development-only")
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Error("HTTP server failed", "error", err)
		os.Exit(1)
	}
}
