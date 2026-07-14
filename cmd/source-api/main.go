package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/keir-research/ai-native-paas/internal/identity/httpauth"
	"github.com/keir-research/ai-native-paas/internal/platformprofile"
	"github.com/keir-research/ai-native-paas/internal/postgresbootstrap"
	"github.com/keir-research/ai-native-paas/internal/source/application"
	"github.com/keir-research/ai-native-paas/internal/source/gitlab"
	"github.com/keir-research/ai-native-paas/internal/source/httpapi"
	"github.com/keir-research/ai-native-paas/internal/source/memory"
	sourcepostgres "github.com/keir-research/ai-native-paas/internal/source/postgres"
	"github.com/keir-research/ai-native-paas/internal/source/support"
	sourcehook "github.com/keir-research/ai-native-paas/internal/source/webhook"
)

func main() {
	profile, err := platformprofile.Parse(os.Getenv("PLATFORM_PROFILE"))
	if err != nil {
		log.Fatal(err)
	}
	ctx := context.Background()
	var (
		store       application.Store
		storageName string
		adapters    []platformprofile.Adapter
	)
	if profile == platformprofile.Production {
		db, openErr := postgresbootstrap.Open(ctx, os.Getenv("DATABASE_URL"))
		if openErr != nil {
			log.Fatal(openErr)
		}
		defer db.Close()
		postgresStore := &sourcepostgres.Store{DB: db, MaxSerializableRetries: 8}
		if migrateErr := postgresbootstrap.WithMigrationLock(ctx, db, "source", postgresStore.Migrate); migrateErr != nil {
			log.Fatal(migrateErr)
		}
		store = postgresStore
		storageName = "postgres"
		adapters = []platformprofile.Adapter{platformprofile.Prod("source-postgres-store"), platformprofile.Prod("gitlab-api"), platformprofile.Prod("verified-identity-middleware")}
	} else {
		store = memory.New()
		storageName = "memory-development-only"
		adapters = []platformprofile.Adapter{platformprofile.Dev("source-memory-store"), platformprofile.Dev("development-identity-headers"), platformprofile.Prod("gitlab-api")}
	}
	if _, err := platformprofile.Validate(string(profile), "source-api", adapters...); err != nil {
		log.Fatal(err)
	}
	clock := support.RealClock{}
	ids := &support.IDs{}
	provider := &gitlab.Client{BaseURL: os.Getenv("GITLAB_URL"), AdminToken: os.Getenv("GITLAB_ADMIN_TOKEN")}
	source := &application.Service{Store: store, Provider: provider, Clock: clock, IDs: ids}
	verifier := sourcehook.Verifier{Secret: []byte(os.Getenv("GITLAB_WEBHOOK_SECRET")), ReplayWindow: 5 * time.Minute, AllowLegacy: os.Getenv("ALLOW_LEGACY_GITLAB_WEBHOOK") == "true", LegacyToken: os.Getenv("GITLAB_LEGACY_WEBHOOK_TOKEN")}
	hooks := &application.WebhookService{Store: store, Provider: provider, Verifier: verifier, Normalizer: sourcehook.Normalizer{Provider: "gitlab"}, Clock: clock, IDs: ids}
	var handler http.Handler = httpapi.Handler{Source: source, Webhooks: hooks}
	handler = (httpauth.Middleware{
		Profile:        profile,
		PublicPaths:    map[string]struct{}{`/healthz`: {}},
		PublicPrefixes: []string{"/hooks/gitlab/"},
	}).Wrap(handler)
	addr := os.Getenv("LISTEN_ADDR")
	if addr == "" {
		addr = ":8081"
	}
	log.Printf("source api listening on %s (storage=%s profile=%s)", addr, storageName, profile)
	log.Fatal(http.ListenAndServe(addr, handler))
}
