package application

import (
	"errors"
	"time"

	commercev2 "github.com/keir-research/ai-native-paas/pkg/contracts/commerce/v2"
	infrastructurev1 "github.com/keir-research/ai-native-paas/pkg/contracts/infrastructure/v1"
)

var (
	ErrConflict         = errors.New("infrastructure conflict")
	ErrNotFound         = errors.New("infrastructure record not found")
	ErrApprovalRequired = errors.New("exact-plan approval is required")
	ErrPermissionDenied = errors.New("infrastructure authorization denied")
)

type UnitPrice struct {
	Meter                    string
	Unit                     string
	ProviderMinorPerQuantity int64
	UnknownMaximumMinor      int64
	Known                    bool
}

type PriceBook struct {
	RateCard commercev2.RateCard
	Prices   map[string]UnitPrice
}

type PlanRecord struct {
	TenantID               string
	IdempotencyKey         string
	IdempotencyFingerprint string
	Summary                infrastructurev1.PlanSummary
	ArtifactDigest         string
	Target                 string
	Estimate               commercev2.CostEstimate
	Reservation            commercev2.ExecutionReservation
	Version                int64
	ApplyStartedAt         time.Time
}

type ApprovalGrant struct {
	GrantID         string
	TenantID        string
	ProjectID       string
	PlanID          string
	PlanHash        string
	EstimateVersion string
	ReservationID   string
	Target          string
	ActorID         string
	ApproverUserID  string
	ExpiresAt       time.Time
	ConsumedAt      time.Time
}

type ApplyMatch struct {
	TenantID         string
	ProjectID        string
	Authorization    infrastructurev1.ApplyAuthorization
	ApprovalRequired bool
	Now              time.Time
}

type Store interface {
	CreatePlan(PlanRecord) (PlanRecord, error)
	GetPlan(tenantID, projectID, planID string) (PlanRecord, error)
	CreateApproval(ApprovalGrant) (ApprovalGrant, error)
	AuthorizeApply(ApplyMatch) (PlanRecord, error)
}

type Clock interface{ Now() time.Time }
type IDs interface{ New(prefix string) string }

type Service struct {
	Store          Store
	Clock          Clock
	IDs            IDs
	Prices         PriceBook
	EstimateTTL    time.Duration
	ReservationTTL time.Duration
}
