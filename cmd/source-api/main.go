package main

import (
	"log"
	"net/http"
	"os"
	"time"

	"github.com/keir-research/ai-native-paas/internal/platformprofile"
	"github.com/keir-research/ai-native-paas/internal/source/application"
	"github.com/keir-research/ai-native-paas/internal/source/gitlab"
	"github.com/keir-research/ai-native-paas/internal/source/httpapi"
	"github.com/keir-research/ai-native-paas/internal/source/memory"
	"github.com/keir-research/ai-native-paas/internal/source/support"
	sourcehook "github.com/keir-research/ai-native-paas/internal/source/webhook"
)

func main() {
	if _, err := platformprofile.Validate(os.Getenv("PLATFORM_PROFILE"), "source-api", platformprofile.Dev("source-memory-store")); err != nil {
		log.Fatal(err)
	}
	store := memory.New()
	clock := support.RealClock{}
	ids := &support.IDs{}
	provider := &gitlab.Client{BaseURL: os.Getenv("GITLAB_URL"), AdminToken: os.Getenv("GITLAB_ADMIN_TOKEN")}
	source := &application.Service{Store: store, Provider: provider, Clock: clock, IDs: ids}
	verifier := sourcehook.Verifier{Secret: []byte(os.Getenv("GITLAB_WEBHOOK_SECRET")), ReplayWindow: 5 * time.Minute, AllowLegacy: os.Getenv("ALLOW_LEGACY_GITLAB_WEBHOOK") == "true", LegacyToken: os.Getenv("GITLAB_LEGACY_WEBHOOK_TOKEN")}
	hooks := &application.WebhookService{Store: store, Provider: provider, Verifier: verifier, Normalizer: sourcehook.Normalizer{Provider: "gitlab"}, Clock: clock, IDs: ids}
	handler := httpapi.Handler{Source: source, Webhooks: hooks}
	addr := os.Getenv("LISTEN_ADDR")
	if addr == "" {
		addr = ":8081"
	}
	log.Printf("source api listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, handler))
}
