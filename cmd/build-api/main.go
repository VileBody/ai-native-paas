package main

import (
	"log"
	"net/http"
	"os"

	"github.com/keir-research/ai-native-paas/internal/build/application"
	"github.com/keir-research/ai-native-paas/internal/build/httpapi"
	"github.com/keir-research/ai-native-paas/internal/build/logs"
	"github.com/keir-research/ai-native-paas/internal/build/memory"
	"github.com/keir-research/ai-native-paas/internal/build/support"
)

func main() {
	service := &application.Service{Store: memory.New(), Logs: logs.New(), Clock: support.Clock{}, IDs: &support.IDs{}, RepositoryBase: os.Getenv("BUILD_REGISTRY_BASE")}
	handler := httpapi.Handler{Build: service}
	address := os.Getenv("LISTEN_ADDR")
	if address == "" {
		address = ":8082"
	}
	log.Printf("build api listening on %s", address)
	log.Fatal(http.ListenAndServe(address, handler))
}
