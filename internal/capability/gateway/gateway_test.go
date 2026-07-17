package gateway

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/keir-research/ai-native-paas/internal/capability/ratelimit"
	capabilitiesv1 "github.com/keir-research/ai-native-paas/pkg/contracts/capabilities/v1"
	commercev1 "github.com/keir-research/ai-native-paas/pkg/contracts/commerce/v1"
)

type fixedClock struct{ now time.Time }

func (c fixedClock) Now() time.Time { return c.now }

type memoryBindings struct {
	binding capabilitiesv1.Binding
	err     error
}

func (m memoryBindings) GetBinding(context.Context, string, string, string) (capabilitiesv1.Binding, bool, error) {
	if m.err != nil {
		return capabilitiesv1.Binding{}, false, m.err
	}
	return m.binding, true, nil
}

type broker struct {
	lease ProviderLease
	err   error
	calls int
}

func (b *broker) Lease(context.Context, capabilitiesv1.Binding) (ProviderLease, error) {
	b.calls++
	if b.err != nil {
		return ProviderLease{}, b.err
	}
	return b.lease, nil
}

type sink struct {
	events []commercev1.UsageEvent
	err    error
}

func (s *sink) Append(_ context.Context, event commercev1.UsageEvent) error {
	if s.err != nil {
		return s.err
	}
	s.events = append(s.events, event)
	return nil
}

func newLimiter(t *testing.T) *ratelimit.Limiter {
	t.Helper()
	limiter, err := ratelimit.New(10, time.Minute, 10)
	if err != nil {
		t.Fatal(err)
	}
	return limiter
}

func testBinding(kind capabilitiesv1.CapabilityKind) capabilitiesv1.Binding {
	return capabilitiesv1.Binding{
		BindingID: "binding-1", ProjectID: "project-1", Kind: kind,
		Endpoint: "https://capability.internal/binding-1", ProjectTokenRef: "bao://project-token",
		BudgetID: "budget-1", RateLimitID: "rate-1", ProviderPolicy: "openrouter-primary",
	}
}

func TestAuthorizeFailsClosedWhenProviderLeaseUnavailable(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 17, 10, 0, 0, 0, time.UTC)
	broker := &broker{err: errors.New("openbao sealed")}
	service := &Service{
		Bindings:    memoryBindings{binding: testBinding(capabilitiesv1.CapabilityLLM)},
		Credentials: broker, Limiter: newLimiter(t), Clock: fixedClock{now: now},
	}

	_, err := service.Authorize(context.Background(), Request{
		TenantID: "tenant-1", ProjectID: "project-1", ActorID: "agent-1",
		BindingID: "binding-1", Kind: capabilitiesv1.CapabilityLLM,
		PresentedTokenRef: "bao://project-token",
	})
	if !errors.Is(err, ErrDependency) {
		t.Fatalf("provider outage did not become a dependency wait: %v", err)
	}
	var dependency *DependencyError
	if !errors.As(err, &dependency) || dependency.Code != "capability_provider_credentials" {
		t.Fatalf("wrong dependency code: %#v", err)
	}
	if broker.calls != 1 {
		t.Fatalf("credential broker calls=%d", broker.calls)
	}
}

func TestAuthorizeReturnsOnlyProjectScopedPublicView(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 17, 10, 0, 0, 0, time.UTC)
	master := "Bearer provider-master-key-never-public"
	service := &Service{
		Bindings:    memoryBindings{binding: testBinding(capabilitiesv1.CapabilityLLM)},
		Credentials: &broker{lease: ProviderLease{Provider: "openrouter", AuthorizationHeader: master, ExpiresAt: now.Add(time.Minute)}},
		Limiter:     newLimiter(t), Clock: fixedClock{now: now},
	}

	decision, err := service.Authorize(context.Background(), Request{
		TenantID: "tenant-1", ProjectID: "project-1", ActorID: "agent-1",
		BindingID: "binding-1", Kind: capabilitiesv1.CapabilityLLM,
		PresentedTokenRef: "bao://project-token",
	})
	if err != nil {
		t.Fatal(err)
	}
	if decision.UpstreamHeaders["authorization"] != master {
		t.Fatalf("private upstream header missing: %#v", decision.UpstreamHeaders)
	}
	public := decision.PublicBinding()
	if public.ProjectTokenRef != "" || public.BudgetID != "" || public.RateLimitID != "" {
		t.Fatalf("public view exposed internal refs: %#v", public)
	}
	if strings.Contains(public.Endpoint+public.ProviderPolicy, "provider-master-key") {
		t.Fatalf("public view leaked provider master credential: %#v", public)
	}
}

func TestIngestUsageMapsProviderFactsToCommerceMeters(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 17, 10, 0, 0, 0, time.UTC)
	usage := &sink{}
	service := &Service{
		Bindings: memoryBindings{binding: testBinding(capabilitiesv1.CapabilityLLM)},
		Usage:    usage, Clock: fixedClock{now: now},
	}

	err := service.IngestUsage(context.Background(), UsageReport{
		TenantID: "tenant-1", ProjectID: "project-1", PeriodID: "period-1",
		Fact: capabilitiesv1.UsageFact{
			UsageID: "usage-1", BindingID: "binding-1", Kind: capabilitiesv1.CapabilityLLM,
			Provider: "openrouter", ProviderRequestID: "req-1", DeduplicationKey: "provider-event-1",
			InputUnits: 123, OutputUnits: 45, OccurredAt: now,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(usage.events) != 2 {
		t.Fatalf("events=%+v", usage.events)
	}
	if usage.events[0].Meter != commercev1.MeterOpenRouterInputTokens || usage.events[0].Quantity != 123 ||
		usage.events[1].Meter != commercev1.MeterOpenRouterOutputTokens || usage.events[1].Quantity != 45 {
		t.Fatalf("usage not mapped to token meters: %+v", usage.events)
	}
	if usage.events[0].IdempotencyKey == usage.events[1].IdempotencyKey || !strings.Contains(usage.events[0].IdempotencyKey, "provider-event-1") {
		t.Fatalf("dedupe keys are not provider-event scoped: %+v", usage.events)
	}
	for _, event := range usage.events {
		if event.ResourceType != "capability.llm_openrouter_compatible" || event.ResourceID != "binding-1" ||
			event.Metadata["provider_request_id"] != "req-1" {
			t.Fatalf("event lost capability attribution: %+v", event)
		}
	}
}
