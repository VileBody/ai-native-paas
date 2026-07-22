package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/keir-research/ai-native-paas/internal/commerce/application"
	"github.com/keir-research/ai-native-paas/internal/commerce/bootstrap"
	commercepostgres "github.com/keir-research/ai-native-paas/internal/commerce/postgres"
	"github.com/keir-research/ai-native-paas/internal/platformprofile"
	"github.com/keir-research/ai-native-paas/internal/postgresbootstrap"
)

type realClock struct{}

func (realClock) Now() time.Time { return time.Now().UTC() }

type randomIDs struct{}

func (randomIDs) NewID(prefix string) string {
	var raw [12]byte
	if _, err := rand.Read(raw[:]); err != nil {
		panic("secure random source unavailable")
	}
	return prefix + "-" + hex.EncodeToString(raw[:])
}

func required(name string) string {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		log.Fatalf("%s is required", name)
	}
	return value
}

func main() {
	if _, err := platformprofile.Validate(os.Getenv("PLATFORM_PROFILE"), "commerce-seed",
		platformprofile.Prod("commerce-postgres-store"),
		platformprofile.Prod("operator-controlled-beta-seed"),
	); err != nil {
		log.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	databaseURL := required("DATABASE_URL")
	tenantID := required("COMMERCE_SEED_TENANT_ID")
	subscriptionID := required("COMMERCE_SEED_SUBSCRIPTION_ID")
	periodID := required("COMMERCE_SEED_BILLING_PERIOD_ID")
	seconds, err := strconv.ParseInt(required("COMMERCE_SEED_WORKSPACE_SECONDS"), 10, 64)
	if err != nil || seconds < 1 {
		log.Fatal("COMMERCE_SEED_WORKSPACE_SECONDS must be a positive integer")
	}
	db, err := postgresbootstrap.Open(ctx, databaseURL)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	store, err := commercepostgres.NewStore(db)
	if err != nil {
		log.Fatal(err)
	}
	if err := postgresbootstrap.WithMigrationLock(ctx, db, "commerce", store.Migrate); err != nil {
		log.Fatal(err)
	}
	service := &application.Service{Store: store, Clock: realClock{}, IDs: randomIDs{}, Ownership: application.DenyAllOwnership{}}
	if err := bootstrap.EnsureControlledBeta(ctx, service, store, bootstrap.ControlledBetaConfig{
		TenantID: tenantID, SubscriptionID: subscriptionID, BillingPeriodID: periodID,
		WorkspaceSeconds: seconds, Now: time.Now().UTC(),
	}); err != nil {
		log.Fatal(err)
	}
	fmt.Println("controlled beta commerce subscription is ready")
}
