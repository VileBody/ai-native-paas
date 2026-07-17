// Package gateway owns provider-neutral capability gateway admission and usage
// translation. It deliberately contains no HTTP client and no provider master
// credential storage; production adapters must supply short-lived leases from
// OpenBao/provider gateways.
package gateway

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/keir-research/ai-native-paas/internal/capability/ratelimit"
	capabilitiesv1 "github.com/keir-research/ai-native-paas/pkg/contracts/capabilities/v1"
	commercev1 "github.com/keir-research/ai-native-paas/pkg/contracts/commerce/v1"
)

var (
	ErrInvalid    = errors.New("invalid capability gateway request")
	ErrForbidden  = errors.New("capability gateway forbidden")
	ErrDependency = errors.New("capability gateway dependency unavailable")
)

type DependencyError struct {
	Code  string
	Cause error
}

func (e *DependencyError) Error() string {
	if e == nil {
		return ErrDependency.Error()
	}
	if e.Code == "" {
		return ErrDependency.Error()
	}
	return "waiting dependency: " + e.Code
}

func (e *DependencyError) Unwrap() error { return ErrDependency }

type BindingStore interface {
	GetBinding(ctx context.Context, tenantID, projectID, bindingID string) (capabilitiesv1.Binding, bool, error)
}

type CredentialBroker interface {
	Lease(ctx context.Context, binding capabilitiesv1.Binding) (ProviderLease, error)
}

type UsageSink interface {
	Append(context.Context, commercev1.UsageEvent) error
}

type Clock interface{ Now() time.Time }

type ProviderLease struct {
	Provider            string
	AuthorizationHeader string
	ExpiresAt           time.Time
}

type Service struct {
	Bindings    BindingStore
	Credentials CredentialBroker
	Limiter     *ratelimit.Limiter
	Usage       UsageSink
	Clock       Clock
}

type Request struct {
	TenantID          string
	ProjectID         string
	ActorID           string
	BindingID         string
	Kind              capabilitiesv1.CapabilityKind
	PresentedTokenRef string
}

type ForwardingDecision struct {
	BindingID       string
	Kind            capabilitiesv1.CapabilityKind
	Endpoint        string
	Provider        string
	ExpiresAt       time.Time
	UpstreamHeaders map[string]string
}

func (d ForwardingDecision) PublicBinding() capabilitiesv1.Binding {
	return capabilitiesv1.Binding{
		BindingID:      d.BindingID,
		Kind:           d.Kind,
		Endpoint:       d.Endpoint,
		ProviderPolicy: d.Provider,
	}
}

func (s *Service) Authorize(ctx context.Context, request Request) (ForwardingDecision, error) {
	if s == nil || s.Bindings == nil || s.Credentials == nil || s.Limiter == nil {
		return ForwardingDecision{}, dependency("capability_gateway_config", nil)
	}
	request.TenantID = strings.TrimSpace(request.TenantID)
	request.ProjectID = strings.TrimSpace(request.ProjectID)
	request.ActorID = strings.TrimSpace(request.ActorID)
	request.BindingID = strings.TrimSpace(request.BindingID)
	request.PresentedTokenRef = strings.TrimSpace(request.PresentedTokenRef)
	if request.TenantID == "" || request.ProjectID == "" || request.ActorID == "" || request.BindingID == "" || request.PresentedTokenRef == "" {
		return ForwardingDecision{}, ErrInvalid
	}
	binding, err := s.binding(ctx, request)
	if err != nil {
		return ForwardingDecision{}, err
	}
	if request.Kind != "" && request.Kind != binding.Kind {
		return ForwardingDecision{}, ErrForbidden
	}
	if request.PresentedTokenRef != binding.ProjectTokenRef {
		return ForwardingDecision{}, ErrForbidden
	}
	if err = s.Limiter.Allow(ratelimit.Scope{TenantID: request.TenantID, ProjectID: request.ProjectID}, s.now()); err != nil {
		return ForwardingDecision{}, err
	}
	lease, err := s.Credentials.Lease(ctx, binding)
	if err != nil {
		return ForwardingDecision{}, dependency("capability_provider_credentials", err)
	}
	if strings.TrimSpace(lease.Provider) == "" || strings.TrimSpace(lease.AuthorizationHeader) == "" || !lease.ExpiresAt.After(s.now()) {
		return ForwardingDecision{}, dependency("capability_provider_credentials", nil)
	}
	return ForwardingDecision{
		BindingID: binding.BindingID, Kind: binding.Kind, Endpoint: binding.Endpoint,
		Provider: lease.Provider, ExpiresAt: lease.ExpiresAt,
		UpstreamHeaders: map[string]string{"authorization": lease.AuthorizationHeader},
	}, nil
}

type UsageReport struct {
	TenantID  string
	ProjectID string
	PeriodID  string
	Fact      capabilitiesv1.UsageFact
}

func (s *Service) IngestUsage(ctx context.Context, report UsageReport) error {
	if s == nil || s.Bindings == nil || s.Usage == nil {
		return dependency("capability_usage_sink", nil)
	}
	report.TenantID = strings.TrimSpace(report.TenantID)
	report.ProjectID = strings.TrimSpace(report.ProjectID)
	report.PeriodID = strings.TrimSpace(report.PeriodID)
	if report.TenantID == "" || report.ProjectID == "" {
		return ErrInvalid
	}
	if err := report.Fact.Validate(); err != nil {
		return ErrInvalid
	}
	binding, err := s.binding(ctx, Request{
		TenantID: report.TenantID, ProjectID: report.ProjectID,
		BindingID: report.Fact.BindingID,
	})
	if err != nil {
		return err
	}
	if binding.Kind != report.Fact.Kind {
		return ErrForbidden
	}
	for _, event := range usageEvents(report, binding) {
		if event.Quantity == 0 {
			continue
		}
		if err := s.Usage.Append(ctx, event); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) binding(ctx context.Context, request Request) (capabilitiesv1.Binding, error) {
	binding, ok, err := s.Bindings.GetBinding(ctx, request.TenantID, request.ProjectID, request.BindingID)
	if err != nil {
		return capabilitiesv1.Binding{}, dependency("capability_binding_store", err)
	}
	if !ok {
		return capabilitiesv1.Binding{}, ErrForbidden
	}
	if err = binding.Validate(); err != nil || binding.ProjectID != request.ProjectID {
		return capabilitiesv1.Binding{}, ErrForbidden
	}
	return binding, nil
}

func (s *Service) now() time.Time {
	if s.Clock == nil {
		return time.Now().UTC()
	}
	return s.Clock.Now().UTC()
}

func dependency(code string, cause error) error {
	return &DependencyError{Code: code, Cause: cause}
}

func usageEvents(report UsageReport, binding capabilitiesv1.Binding) []commercev1.UsageEvent {
	base := commercev1.UsageEvent{
		TenantID: report.TenantID, PeriodID: report.PeriodID,
		ResourceType: resourceType(binding.Kind), ResourceID: binding.BindingID,
		Kind: commercev1.UsageStandard, OccurredAt: report.Fact.OccurredAt.UTC(),
		WindowStart: report.Fact.OccurredAt.UTC().Add(-time.Minute), WindowEnd: report.Fact.OccurredAt.UTC(),
		Metadata: map[string]string{
			"provider":            report.Fact.Provider,
			"provider_request_id": report.Fact.ProviderRequestID,
			"deduplication_key":   report.Fact.DeduplicationKey,
		},
	}
	event := func(meter commercev1.Meter, quantity int64) commercev1.UsageEvent {
		next := base
		next.Meter = meter
		next.Quantity = quantity
		next.IdempotencyKey = fmt.Sprintf("capability:%s:%s:%s", report.Fact.Provider, report.Fact.DeduplicationKey, meter)
		return next
	}
	switch binding.Kind {
	case capabilitiesv1.CapabilityLLM:
		return []commercev1.UsageEvent{
			event(commercev1.MeterOpenRouterInputTokens, report.Fact.InputUnits),
			event(commercev1.MeterOpenRouterOutputTokens, report.Fact.OutputUnits),
		}
	case capabilitiesv1.CapabilityApify:
		return []commercev1.UsageEvent{
			event(commercev1.MeterApifyActorRuns, report.Fact.InputUnits),
			event(commercev1.MeterApifyStorageBytes, report.Fact.OutputUnits),
		}
	case capabilitiesv1.CapabilityBrightData:
		return []commercev1.UsageEvent{
			event(commercev1.MeterBrightDataRequests, report.Fact.InputUnits),
			event(commercev1.MeterBrightDataTransferBytes, report.Fact.OutputUnits),
		}
	default:
		return nil
	}
}

func resourceType(kind capabilitiesv1.CapabilityKind) string {
	return "capability." + strings.NewReplacer(".", "_", "-", "_").Replace(string(kind))
}
