package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/keir-research/ai-native-paas/internal/platformprofile"
	"github.com/keir-research/ai-native-paas/internal/remotestate"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if _, err := platformprofile.Validate(os.Getenv("PLATFORM_PROFILE"), "state-service", platformprofile.Prod("postgres-lock-repository"), platformprofile.Prod("s3-encrypted-blob-store"), platformprofile.Prod("scoped-expiring-basic-auth")); err != nil {
		logger.Error("invalid runtime profile", "error", err)
		os.Exit(1)
	}

	databaseURL := required(logger, "DATABASE_URL")
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		logger.Error("open PostgreSQL", "error", err)
		os.Exit(1)
	}
	defer db.Close()
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(30 * time.Minute)

	startup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := db.PingContext(startup); err != nil {
		logger.Error("connect PostgreSQL", "error", err)
		os.Exit(1)
	}
	lockTTL := envDuration(logger, "STATE_LOCK_TTL", 2*time.Hour)
	repository, err := remotestate.NewPostgresRepository(db, lockTTL)
	if err != nil {
		logger.Error("initialize lock repository", "error", err)
		os.Exit(1)
	}
	if err := repository.Migrate(startup); err != nil {
		logger.Error("migrate state service", "error", err)
		os.Exit(1)
	}

	blobs, err := remotestate.NewS3BlobStore(remotestate.S3Config{
		Endpoint:  required(logger, "STATE_S3_ENDPOINT"),
		Region:    env("STATE_S3_REGION", "ru-1"),
		Bucket:    required(logger, "STATE_S3_BUCKET"),
		Prefix:    env("STATE_S3_PREFIX", "platform-http-state"),
		AccessKey: required(logger, "STATE_S3_ACCESS_KEY"),
		SecretKey: required(logger, "STATE_S3_SECRET_KEY"),
	})
	if err != nil {
		logger.Error("initialize S3 blob store", "error", err)
		os.Exit(1)
	}
	service, err := remotestate.NewService(blobs, repository)
	if err != nil {
		logger.Error("initialize state service", "error", err)
		os.Exit(1)
	}
	credentialConfigs := loadCredentialConfigs(logger)
	credentials := make([]*remotestate.StaticCredential, 0, len(credentialConfigs))
	for _, config := range credentialConfigs {
		if err := repository.EnsureNamespace(startup, remotestate.NamespaceOwner{Namespace: config.Namespace, TenantID: config.TenantID, ProjectID: config.ProjectID}); err != nil {
			logger.Error("ensure bootstrap state namespace", "namespace", config.Namespace, "error", err)
			os.Exit(1)
		}
		credential, err := remotestate.NewStaticCredential(config.Username, config.Password, remotestate.Claims{
			Namespace:       config.Namespace,
			TenantID:        config.TenantID,
			ProjectID:       config.ProjectID,
			Actor:           config.Actor,
			ExpiresAt:       config.ExpiresAt,
			CanRecoverStale: config.CanRecoverStale,
		})
		if err != nil {
			logger.Error("initialize bootstrap credential", "namespace", config.Namespace, "error", err)
			os.Exit(1)
		}
		credentials = append(credentials, credential)
	}
	credentialSet, err := remotestate.NewCredentialSet(credentials...)
	if err != nil {
		logger.Error("initialize credential set", "error", err)
		os.Exit(1)
	}
	handler, err := remotestate.NewHandler(service, credentialSet)
	if err != nil {
		logger.Error("initialize state HTTP handler", "error", err)
		os.Exit(1)
	}

	server := &http.Server{
		Addr:              env("STATE_HTTP_ADDR", ":8080"),
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       70 * time.Second,
		WriteTimeout:      70 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	logger.Info("state service listening", "address", server.Addr, "namespace_count", len(credentialConfigs), "profile", env("PLATFORM_PROFILE", "development"))
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Error("state service failed", "error", err)
		os.Exit(1)
	}
}

type credentialConfig struct {
	Username        string    `json:"username"`
	Password        string    `json:"password"`
	Namespace       string    `json:"namespace"`
	TenantID        string    `json:"tenant_id"`
	ProjectID       string    `json:"project_id"`
	Actor           string    `json:"actor"`
	ExpiresAt       time.Time `json:"expires_at"`
	CanRecoverStale bool      `json:"can_recover_stale,omitempty"`
}

func loadCredentialConfigs(logger *slog.Logger) []credentialConfig {
	if raw := os.Getenv("STATE_CREDENTIALS_JSON"); raw != "" {
		var configs []credentialConfig
		if err := json.Unmarshal([]byte(raw), &configs); err != nil || len(configs) == 0 {
			logger.Error("STATE_CREDENTIALS_JSON must contain a non-empty credential array")
			os.Exit(1)
		}
		for _, config := range configs {
			if config.Username == "" || config.Password == "" || config.Namespace == "" || config.TenantID == "" || config.ProjectID == "" || config.Actor == "" || !time.Now().UTC().Before(config.ExpiresAt) {
				logger.Error("state credential entry is incomplete or expired", "namespace", config.Namespace)
				os.Exit(1)
			}
		}
		return configs
	}
	expiresAt, err := time.Parse(time.RFC3339, required(logger, "STATE_BOOTSTRAP_EXPIRES_AT"))
	if err != nil || !time.Now().UTC().Before(expiresAt) {
		logger.Error("bootstrap credential expiry must be a future RFC3339 timestamp")
		os.Exit(1)
	}
	return []credentialConfig{{
		Username:  required(logger, "STATE_BOOTSTRAP_USERNAME"),
		Password:  required(logger, "STATE_BOOTSTRAP_PASSWORD"),
		Namespace: required(logger, "STATE_BOOTSTRAP_NAMESPACE"),
		TenantID:  required(logger, "STATE_BOOTSTRAP_TENANT_ID"),
		ProjectID: required(logger, "STATE_BOOTSTRAP_PROJECT_ID"),
		Actor:     required(logger, "STATE_BOOTSTRAP_ACTOR"),
		ExpiresAt: expiresAt,
	}}
}

func required(logger *slog.Logger, name string) string {
	value := os.Getenv(name)
	if value == "" {
		logger.Error("required environment variable is missing", "name", name)
		os.Exit(1)
	}
	return value
}

func env(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func envDuration(logger *slog.Logger, name string, fallback time.Duration) time.Duration {
	value := os.Getenv(name)
	if value == "" {
		return fallback
	}
	duration, err := time.ParseDuration(value)
	if err != nil {
		if seconds, parseErr := strconv.Atoi(value); parseErr == nil {
			return time.Duration(seconds) * time.Second
		}
		logger.Error("invalid duration", "name", name)
		os.Exit(1)
	}
	return duration
}
