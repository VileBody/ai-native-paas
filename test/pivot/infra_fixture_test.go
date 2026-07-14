package pivot_test

import (
	"fmt"
	"strings"
	"sync"
	"time"

	infraapp "github.com/keir-research/ai-native-paas/internal/infrastructure/application"
	inframemory "github.com/keir-research/ai-native-paas/internal/infrastructure/memory"
	commercev2 "github.com/keir-research/ai-native-paas/pkg/contracts/commerce/v2"
)

type fixedClock struct{ now time.Time }

func (c fixedClock) Now() time.Time { return c.now }

type sequenceIDs struct {
	mu sync.Mutex
	n  int
}

func (i *sequenceIDs) New(prefix string) string {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.n++
	return fmt.Sprintf("%s-%d", prefix, i.n)
}

func infrastructureFixture() (*infraapp.Service, *inframemory.Store, time.Time) {
	now := time.Date(2026, 7, 14, 9, 30, 0, 0, time.UTC)
	store := inframemory.New()
	service := &infraapp.Service{
		Store: store, Clock: fixedClock{now: now}, IDs: &sequenceIDs{},
		Prices: infraapp.PriceBook{
			RateCard: commercev2.RateCard{
				RateCardID: "beta-25", Version: "2026-07-14", MarkupBasisPoints: 2500,
				Currency: "RUB", PriceSnapshotID: "timeweb-msk-2026-07-14",
			},
			Prices: map[string]infraapp.UnitPrice{
				"twc_server": {Meter: "timeweb.server.month", Unit: "server-month", ProviderMinorPerQuantity: 100, Known: true},
			},
		},
		EstimateTTL: 30 * time.Minute, ReservationTTL: 20 * time.Minute,
	}
	return service, store, now
}

func planCommand(key, target, planJSON string) infraapp.PlanCommand {
	return infraapp.PlanCommand{
		TenantID: "tenant-1", ProjectID: "project-1", ActorID: "agent-1", WorkspaceID: "workspace-1",
		Target: target, SourceSHA: strings.Repeat("a", 40), IdempotencyKey: key,
		ArtifactDigest: "sha256:" + strings.Repeat("b", 64), StateGeneration: 7,
		PlanJSON: []byte(planJSON),
	}
}

const twoResourcePlan = `{
  "format_version":"1.2",
  "terraform_version":"1.12.4",
  "resource_changes":[
    {"address":"twc_server.app","provider_name":"registry.opentofu.org/timeweb-cloud/timeweb-cloud","type":"twc_server","change":{"actions":["create"],"after":{"token":"not-part-of-summary"}}},
    {"address":"unknown_cache.app","provider_name":"example/unknown","type":"unknown_cache","change":{"actions":["create"]}}
  ]
}`
