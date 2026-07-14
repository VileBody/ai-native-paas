package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/keir-research/ai-native-paas/internal/agent/enrollment"
	enrollmenthttp "github.com/keir-research/ai-native-paas/internal/agent/enrollment/httpapi"
	enrollmentpostgres "github.com/keir-research/ai-native-paas/internal/agent/enrollment/postgres"
	agentsupport "github.com/keir-research/ai-native-paas/internal/agent/support"
	"github.com/keir-research/ai-native-paas/internal/identity/httpauth"
	"github.com/keir-research/ai-native-paas/internal/identity/oidcverify"
	infraapp "github.com/keir-research/ai-native-paas/internal/infrastructure/application"
	infraconfig "github.com/keir-research/ai-native-paas/internal/infrastructure/config"
	infrapostgres "github.com/keir-research/ai-native-paas/internal/infrastructure/postgres"
	"github.com/keir-research/ai-native-paas/internal/platformprofile"
	"github.com/keir-research/ai-native-paas/internal/postgresbootstrap"
	projectapp "github.com/keir-research/ai-native-paas/internal/project/application"
	projecthttp "github.com/keir-research/ai-native-paas/internal/project/httpapi"
	projectmcp "github.com/keir-research/ai-native-paas/internal/project/mcp"
	sourceapp "github.com/keir-research/ai-native-paas/internal/source/application"
	"github.com/keir-research/ai-native-paas/internal/source/gitlab"
	sourcepostgres "github.com/keir-research/ai-native-paas/internal/source/postgres"
	sourcesupport "github.com/keir-research/ai-native-paas/internal/source/support"
	"github.com/keir-research/ai-native-paas/internal/workspace"
	workspacepostgres "github.com/keir-research/ai-native-paas/internal/workspace/postgres"
	agentv2 "github.com/keir-research/ai-native-paas/pkg/contracts/agent/v2"
)

func main() {
	profile, err := platformprofile.Parse(os.Getenv("PLATFORM_PROFILE"))
	if err != nil {
		log.Fatal(err)
	}
	if profile != platformprofile.Production {
		log.Fatal("project-api only starts with PLATFORM_PROFILE=production")
	}
	if _, err := platformprofile.Validate(string(profile), "project-api",
		platformprofile.Prod("source-postgres-store"),
		platformprofile.Prod("agent-enrollment-postgres-store"),
		platformprofile.Prod("gitlab-api"),
		platformprofile.Prod("oidc-jwks-verifier"),
		platformprofile.Prod("postgres-membership-resolver"),
		platformprofile.Prod("workspace-postgres-intent-store"),
		platformprofile.Prod("infrastructure-postgres-exact-plan-gate"),
		platformprofile.Prod("versioned-beta-rate-card"),
		platformprofile.Prod("governed-workspace-git-mutations"),
		platformprofile.Prod("exact-source-change-approval"),
	); err != nil {
		log.Fatal(err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	db, err := postgresbootstrap.Open(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	sourceStore := &sourcepostgres.Store{DB: db, MaxSerializableRetries: 32}
	if err := postgresbootstrap.WithMigrationLock(ctx, db, "source", sourceStore.Migrate); err != nil {
		log.Fatal(err)
	}
	workspaceStore := &workspacepostgres.Store{DB: db}
	if err := postgresbootstrap.WithMigrationLock(ctx, db, "workspace", workspaceStore.Migrate); err != nil {
		log.Fatal(err)
	}
	infrastructureStore := &infrapostgres.Store{DB: db}
	if err := postgresbootstrap.WithMigrationLock(ctx, db, "infrastructure", infrastructureStore.Migrate); err != nil {
		log.Fatal(err)
	}
	enrollmentStore, err := enrollmentpostgres.New(db)
	if err != nil {
		log.Fatal(err)
	}
	if err := postgresbootstrap.WithMigrationLock(ctx, db, "agent-enrollment", enrollmentStore.Migrate); err != nil {
		log.Fatal(err)
	}
	memberships, err := oidcverify.NewPostgresResolver(db)
	if err != nil {
		log.Fatal(err)
	}
	if err := postgresbootstrap.WithMigrationLock(ctx, db, "platform-identity", memberships.Migrate); err != nil {
		log.Fatal(err)
	}

	gitLabToken, err := readSecretFile("GITLAB_ADMIN_TOKEN_FILE", 16<<10)
	if err != nil {
		log.Fatal(err)
	}
	signingKey, err := readSecretFile("ENROLLMENT_SIGNING_KEY_FILE", 4<<10)
	if err != nil {
		log.Fatal(err)
	}
	if len(signingKey) < 32 {
		log.Fatal("enrollment signing key must contain at least 32 bytes")
	}
	namespaceID, err := strconv.ParseInt(strings.TrimSpace(os.Getenv("GITLAB_NAMESPACE_ID")), 10, 64)
	if err != nil || namespaceID <= 0 {
		log.Fatal("GITLAB_NAMESPACE_ID must be a positive integer")
	}

	gitLabURL := strings.TrimSpace(os.Getenv("GITLAB_URL"))
	if gitLabURL == "" {
		gitLabURL = "https://gitlab.com"
	}
	gitLab := &gitlab.Client{BaseURL: gitLabURL, AdminToken: string(gitLabToken)}
	source := &sourceapp.Service{Store: sourceStore, Provider: gitLab, Clock: sourcesupport.RealClock{}, IDs: &sourcesupport.IDs{}}
	clock := agentsupport.Clock{}
	runtimeIDs := &agentsupport.IDs{}
	enrollmentService := &enrollment.Service{
		Store: enrollmentStore, Clock: clock, IDs: enrollmentIDs{inner: runtimeIDs},
		Secrets: enrollment.CryptoSecrets{}, Signer: enrollment.HMACSigner{Key: append([]byte(nil), signingKey...)},
	}
	workspaceService := &workspace.Service{Store: workspaceStore, Clock: clock, IDs: enrollmentIDs{inner: runtimeIDs}, Policy: workspace.DefaultCommandPolicy()}
	priceBook, err := infraconfig.LoadPriceBook(os.Getenv("INFRASTRUCTURE_PRICE_BOOK_FILE"))
	if err != nil {
		log.Fatal(err)
	}
	infrastructureService := &infraapp.Service{
		Store: infrastructureStore, Clock: clock, IDs: enrollmentIDs{inner: runtimeIDs}, Prices: priceBook,
		EstimateTTL: 30 * time.Minute, ReservationTTL: 20 * time.Minute,
	}
	projects := &projectapp.Service{
		Source: source, Bootstrapper: gitLab, Enrollment: enrollmentService,
		GitLabNamespaceID: namespaceID, MCPBaseURL: os.Getenv("MCP_BASE_URL"), WorkspaceImageDigest: os.Getenv("WORKSPACE_IMAGE_DIGEST"),
	}
	oidcVerifier, err := oidcverify.New(ctx, os.Getenv("OIDC_ISSUER"), os.Getenv("OIDC_CLIENT_ID"), memberships)
	if err != nil {
		log.Fatal(err)
	}

	projectHandler := projecthttp.Handler{Projects: projects, Infrastructure: infrastructureService, SourceChanges: workspaceService, MaxBodyBytes: 64 << 10}
	enrollmentHandler := enrollmenthttp.Handler{Enrollment: enrollmentService, MaxBodyBytes: 64 << 10}
	mcpHandler := projectmcp.Handler{Enrollment: enrollmentService, Projects: source, Workspaces: workspaceService, Infrastructure: infrastructureService, SourceChanges: workspaceService, MaxBodyBytes: agentv2.MaximumArgumentsBytes + (64 << 10)}
	humanHandler := (httpauth.Middleware{
		Profile: profile, OIDC: oidcVerifier, PublicPaths: map[string]struct{}{`/healthz`: {}},
	}).Wrap(projectHandler)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/v2/agent/"):
			enrollmentHandler.ServeHTTP(w, r)
		case strings.HasPrefix(r.URL.Path, "/projects/") && strings.Contains(r.URL.Path, "/mcp/v2/"):
			mcpHandler.ServeHTTP(w, r)
		default:
			humanHandler.ServeHTTP(w, r)
		}
	})

	server := &http.Server{
		Addr: env("PROJECT_API_ADDR", ":8088"), Handler: handler,
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second,
		WriteTimeout: 60 * time.Second, IdleTimeout: 60 * time.Second,
	}
	go func() {
		log.Printf("project API listening on %s (production PostgreSQL/GitLab/OIDC)", server.Addr)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("project API stopped unexpectedly: %v", err)
			stop()
		}
	}()
	<-ctx.Done()
	shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdown); err != nil {
		log.Printf("project API shutdown: %v", err)
	}
}

type enrollmentIDs struct{ inner *agentsupport.IDs }

func (i enrollmentIDs) New(prefix string) string { return i.inner.NewID(prefix) }

func readSecretFile(environmentName string, maxBytes int64) ([]byte, error) {
	path := strings.TrimSpace(os.Getenv(environmentName))
	if path == "" {
		return nil, fmt.Errorf("%s is required", environmentName)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", environmentName, err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if info.Size() <= 0 || info.Size() > maxBytes {
		return nil, fmt.Errorf("%s has invalid size", environmentName)
	}
	raw := make([]byte, info.Size())
	if _, err := io.ReadFull(file, raw); err != nil {
		return nil, err
	}
	raw = []byte(strings.TrimSpace(string(raw)))
	if len(raw) == 0 {
		return nil, fmt.Errorf("%s is empty", environmentName)
	}
	return raw, nil
}

func env(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}
