package v1

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"
)

type Meter string

const (
	MeterRuntimeUnitSeconds         Meter = "runtime.unit_seconds"
	MeterRuntimeExtraReplicaSeconds Meter = "runtime.extra_replica_seconds"
	MeterBuildCPUSeconds            Meter = "build.cpu_seconds"
	MeterBuildMemoryGiBSeconds      Meter = "build.memory_gib_seconds"
	MeterBuildDockerVMSeconds       Meter = "build.docker_vm_seconds"
	MeterArtifactStorageGiBHours    Meter = "artifact.storage_gib_hours"
	MeterPersistentStorageGiBHours  Meter = "persistent.storage_gib_hours"
	MeterDatabasePlanSeconds        Meter = "database.plan_seconds"
	MeterObjectStorageGiBHours      Meter = "object_storage.gib_hours"
	MeterEgressBytes                Meter = "egress.bytes"
	MeterLogsIngestedBytes          Meter = "logs.ingested_bytes"
	MeterLogsRetainedGiBHours       Meter = "logs.retained_gib_hours"
	MeterOpenRouterInputTokens      Meter = "capability.openrouter.input_tokens"
	MeterOpenRouterOutputTokens     Meter = "capability.openrouter.output_tokens"
	MeterApifyActorRuns             Meter = "capability.apify.actor_runs"
	MeterApifyStorageBytes          Meter = "capability.apify.storage_bytes"
	MeterBrightDataRequests         Meter = "capability.bright_data.requests"
	MeterBrightDataTransferBytes    Meter = "capability.bright_data.transfer_bytes"
)

var meterCatalog = []Meter{
	MeterRuntimeUnitSeconds, MeterRuntimeExtraReplicaSeconds,
	MeterBuildCPUSeconds, MeterBuildMemoryGiBSeconds, MeterBuildDockerVMSeconds,
	MeterArtifactStorageGiBHours, MeterPersistentStorageGiBHours,
	MeterDatabasePlanSeconds, MeterObjectStorageGiBHours,
	MeterEgressBytes, MeterLogsIngestedBytes, MeterLogsRetainedGiBHours,
	MeterOpenRouterInputTokens, MeterOpenRouterOutputTokens,
	MeterApifyActorRuns, MeterApifyStorageBytes,
	MeterBrightDataRequests, MeterBrightDataTransferBytes,
}

func MeterCatalog() []Meter { return append([]Meter(nil), meterCatalog...) }
func ValidMeter(m Meter) bool {
	for _, item := range meterCatalog {
		if m == item {
			return true
		}
	}
	return false
}

type UsageKind string

const (
	UsageStandard   UsageKind = "STANDARD"
	UsageCredit     UsageKind = "CREDIT"
	UsageCorrection UsageKind = "CORRECTION"
)

type Price struct {
	MinorUnits  int64 `json:"minor_units"`
	PerQuantity int64 `json:"per_quantity"`
}

func (p Price) Validate() error {
	if p.MinorUnits < 0 || p.PerQuantity <= 0 {
		return errors.New("invalid integer price")
	}
	return nil
}

type PlanSpec struct {
	Currency                string           `json:"currency"`
	Features                map[string]bool  `json:"features"`
	Quotas                  map[string]int64 `json:"quotas"`
	Prices                  map[Meter]Price  `json:"prices"`
	Included                map[Meter]int64  `json:"included,omitempty"`
	ChargeUserBuildFailures bool             `json:"charge_user_build_failures"`
}

func (s PlanSpec) Validate() error {
	if len(s.Currency) != 3 || strings.ToUpper(s.Currency) != s.Currency {
		return errors.New("currency must be uppercase ISO-like code")
	}
	for key, value := range s.Quotas {
		if strings.TrimSpace(key) == "" || value < 0 {
			return errors.New("invalid quota")
		}
	}
	for meter, price := range s.Prices {
		if !ValidMeter(meter) || price.Validate() != nil {
			return errors.New("invalid meter price")
		}
	}
	for meter, quantity := range s.Included {
		if !ValidMeter(meter) || quantity < 0 {
			return errors.New("invalid included allowance")
		}
	}
	return nil
}

type EntitlementRequest struct {
	TenantID string    `json:"tenant_id"`
	Feature  string    `json:"feature"`
	Resource string    `json:"resource,omitempty"`
	Quantity int64     `json:"quantity,omitempty"`
	At       time.Time `json:"at"`
}
type EntitlementDecision struct {
	Allowed       bool   `json:"allowed"`
	Reason        string `json:"reason"`
	PolicyVersion string `json:"policy_version"`
	PlanVersionID string `json:"plan_version_id"`
	Limit         int64  `json:"limit,omitempty"`
	Remaining     int64  `json:"remaining,omitempty"`
}
type QuotaRequest struct {
	TenantID       string    `json:"tenant_id"`
	ProjectID      string    `json:"project_id,omitempty"`
	Resource       string    `json:"resource"`
	Quantity       int64     `json:"quantity"`
	IdempotencyKey string    `json:"idempotency_key"`
	ExpiresAt      time.Time `json:"expires_at"`
	At             time.Time `json:"at"`
}
type QuotaReservation struct {
	ID               string    `json:"id"`
	TenantID         string    `json:"tenant_id"`
	ProjectID        string    `json:"project_id,omitempty"`
	Resource         string    `json:"resource"`
	PolicyVersion    string    `json:"policy_version"`
	Reason           string    `json:"reason,omitempty"`
	Quantity         int64     `json:"quantity"`
	SettledQuantity  int64     `json:"settled_quantity,omitempty"`
	ReleasedQuantity int64     `json:"released_quantity,omitempty"`
	State            string    `json:"state"`
	ExpiresAt        time.Time `json:"expires_at"`
	Version          int64     `json:"version"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}
type UsageEvent struct {
	ID             string            `json:"id"`
	TenantID       string            `json:"tenant_id"`
	PeriodID       string            `json:"period_id"`
	ResourceType   string            `json:"resource_type"`
	ResourceID     string            `json:"resource_id"`
	Meter          Meter             `json:"meter"`
	Kind           UsageKind         `json:"kind"`
	Quantity       int64             `json:"quantity"`
	IdempotencyKey string            `json:"idempotency_key"`
	OccurredAt     time.Time         `json:"occurred_at"`
	WindowStart    time.Time         `json:"window_start"`
	WindowEnd      time.Time         `json:"window_end"`
	Metadata       map[string]string `json:"metadata,omitempty"`
	Version        int64             `json:"version"`
	CreatedAt      time.Time         `json:"created_at"`
}
type RatedUsage struct {
	TenantID         string    `json:"tenant_id"`
	PeriodID         string    `json:"period_id"`
	ResourceType     string    `json:"resource_type"`
	ResourceID       string    `json:"resource_id"`
	Meter            Meter     `json:"meter"`
	Kind             UsageKind `json:"kind"`
	Quantity         int64     `json:"quantity"`
	BillableQuantity int64     `json:"billable_quantity"`
	AmountMinorUnits int64     `json:"amount_minor_units"`
	Currency         string    `json:"currency"`
	PlanVersionID    string    `json:"plan_version_id"`
	PolicyVersion    string    `json:"policy_version"`
}
type InvoiceLine struct {
	ResourceType     string    `json:"resource_type"`
	ResourceID       string    `json:"resource_id"`
	Meter            Meter     `json:"meter"`
	Kind             UsageKind `json:"kind"`
	Quantity         int64     `json:"quantity"`
	BillableQuantity int64     `json:"billable_quantity"`
	AmountMinorUnits int64     `json:"amount_minor_units"`
}
type InvoicePreview struct {
	TenantID        string        `json:"tenant_id"`
	PeriodID        string        `json:"period_id"`
	Currency        string        `json:"currency"`
	PlanVersionID   string        `json:"plan_version_id"`
	PolicyVersion   string        `json:"policy_version"`
	Lines           []InvoiceLine `json:"lines"`
	TotalMinorUnits int64         `json:"total_minor_units"`
}

func (p *InvoicePreview) Normalize() {
	sort.Slice(p.Lines, func(i, j int) bool {
		a, b := p.Lines[i], p.Lines[j]
		if a.ResourceType != b.ResourceType {
			return a.ResourceType < b.ResourceType
		}
		if a.ResourceID != b.ResourceID {
			return a.ResourceID < b.ResourceID
		}
		if a.Meter != b.Meter {
			return a.Meter < b.Meter
		}
		return a.Kind < b.Kind
	})
}

type RuntimeObservation struct {
	At         time.Time `json:"at"`
	Replicas   int64     `json:"replicas"`
	UnitWeight int64     `json:"unit_weight"`
	Suspended  bool      `json:"suspended"`
}
type BuildOutcome string

const (
	BuildSucceeded      BuildOutcome = "SUCCEEDED"
	BuildUserFailed     BuildOutcome = "USER_FAILED"
	BuildPlatformFailed BuildOutcome = "PLATFORM_FAILED"
	BuildCanceled       BuildOutcome = "CANCELED"
)

type BuildUsage struct {
	TenantID, BuildID                             string
	Outcome                                       BuildOutcome
	StartedAt, FinishedAt                         time.Time
	CPUSeconds, MemoryGiBSeconds, DockerVMSeconds int64
	IdempotencyKey                                string
}
type CommercialState string

const (
	CommercialActive    CommercialState = "ACTIVE"
	CommercialGrace     CommercialState = "GRACE"
	CommercialSuspended CommercialState = "SUSPENDED"
	CommercialCanceled  CommercialState = "CANCELED"
)

type RuntimeIntent struct {
	TenantID              string    `json:"tenant_id"`
	Action                string    `json:"action"`
	RetainManagedServices bool      `json:"retain_managed_services"`
	At                    time.Time `json:"at"`
}

type EntitlementService interface {
	Check(context.Context, EntitlementRequest) (EntitlementDecision, error)
	Reserve(context.Context, QuotaRequest) (QuotaReservation, error)
	Commit(context.Context, string) error
	Release(context.Context, string) error
}
type UsageSink interface {
	Append(context.Context, UsageEvent) error
}
