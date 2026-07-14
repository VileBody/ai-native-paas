package application

import (
	"context"
	"time"

	"github.com/keir-research/ai-native-paas/internal/commerce/domain"
	commercev1 "github.com/keir-research/ai-native-paas/pkg/contracts/commerce/v1"
)

type Clock interface{ Now() time.Time }
type IDGenerator interface{ NewID(prefix string) string }
type ResourceOwnership interface {
	Owns(ctx context.Context, tenantID, resourceType, resourceID string) (bool, error)
}
type ProjectResourceOwnership interface {
	OwnsProject(ctx context.Context, tenantID, projectID, resourceType, resourceID string) (bool, error)
}
type AllowAllOwnership struct{}

func (AllowAllOwnership) Owns(context.Context, string, string, string) (bool, error) {
	return true, nil
}
func (AllowAllOwnership) OwnsProject(context.Context, string, string, string, string) (bool, error) {
	return true, nil
}

type DenyAllOwnership struct{}

func (DenyAllOwnership) Owns(context.Context, string, string, string) (bool, error) {
	return false, nil
}
func (DenyAllOwnership) OwnsProject(context.Context, string, string, string, string) (bool, error) {
	return false, nil
}

type IdempotencyRecord struct {
	TenantID, Scope, Key, Fingerprint, ResourceID string
	CreatedAt                                     time.Time
}
type OutboxRecord struct {
	ID, TenantID, Topic, AggregateID string
	Payload                          []byte
	CreatedAt                        time.Time
}
type AuditRecord struct {
	ID, TenantID, ActorID, Action, ResourceType, ResourceID string
	Data                                                    []byte
	CreatedAt                                               time.Time
}
type ReconciliationAlert struct {
	ID, TenantID, PeriodID, Meter, ResourceID, Reason string
	Drift                                             int64
	CreatedAt                                         time.Time
}

type Tx interface {
	GetPlanDefinition(string) (domain.PlanDefinition, bool)
	InsertPlanDefinition(domain.PlanDefinition) error
	UpdatePlanDefinition(domain.PlanDefinition, int64) error
	GetPlanVersion(string) (domain.PlanVersion, bool)
	ListPlanVersions(string) []domain.PlanVersion
	InsertPlanVersion(domain.PlanVersion) error
	UpdatePlanVersion(domain.PlanVersion, int64) error
	GetSubscription(string) (domain.Subscription, bool)
	FindSubscriptionByTenant(string) (domain.Subscription, bool)
	InsertSubscription(domain.Subscription) error
	UpdateSubscription(domain.Subscription, int64) error
	GetBillingPeriod(string) (domain.BillingPeriod, bool)
	ListBillingPeriods(string) []domain.BillingPeriod
	InsertBillingPeriod(domain.BillingPeriod) error
	UpdateBillingPeriod(domain.BillingPeriod, int64) error
	GetCommercialAccount(string) (domain.CommercialAccount, bool)
	InsertCommercialAccount(domain.CommercialAccount) error
	UpdateCommercialAccount(domain.CommercialAccount, int64) error
	GetQuotaReservation(string) (domain.QuotaReservation, bool)
	FindQuotaReservation(string, string) (domain.QuotaReservation, bool)
	ListQuotaReservations(string, string) []domain.QuotaReservation
	InsertQuotaReservation(domain.QuotaReservation) error
	UpdateQuotaReservation(domain.QuotaReservation, int64) error
	FindUsageByKey(string, string) (commercev1.UsageEvent, bool)
	ListUsage(string, string) []commercev1.UsageEvent
	InsertUsage(commercev1.UsageEvent) error
	GetIdempotency(string, string, string) (IdempotencyRecord, bool)
	InsertIdempotency(IdempotencyRecord) error
	AppendOutbox(OutboxRecord) error
	AppendAudit(AuditRecord) error
	AppendAlert(ReconciliationAlert) error
	ListAlerts(string, string) []ReconciliationAlert
}
type Store interface {
	Transact(context.Context, func(Tx) error) error
}

type Service struct {
	Store               Store
	Clock               Clock
	IDs                 IDGenerator
	Ownership           ResourceOwnership
	DriftAlertThreshold int64
}

func (s *Service) now() time.Time {
	if s.Clock == nil {
		return time.Now().UTC()
	}
	return s.Clock.Now().UTC()
}
func (s *Service) newID(prefix string) string {
	if s.IDs == nil {
		return prefix + "-generated"
	}
	return s.IDs.NewID(prefix)
}
func (s *Service) ownership() ResourceOwnership {
	if s.Ownership == nil {
		return DenyAllOwnership{}
	}
	return s.Ownership
}
func (s *Service) ownsProject(ctx context.Context, tenantID, projectID, resourceType, resourceID string) (bool, error) {
	ownership, ok := s.ownership().(ProjectResourceOwnership)
	if !ok {
		return false, nil
	}
	return ownership.OwnsProject(ctx, tenantID, projectID, resourceType, resourceID)
}
func (s *Service) require() error {
	if s == nil || s.Store == nil {
		return domain.NewError(domain.CodeUnavailable, "commerce store is unavailable")
	}
	return nil
}

var _ commercev1.EntitlementService = (*Service)(nil)
var _ commercev1.UsageSink = (*Service)(nil)
