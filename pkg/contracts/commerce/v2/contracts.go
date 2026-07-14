// Package v2 defines beta estimates, reservations, immutable rate cards and usage ledgers.
package v2

import (
	"errors"
	"regexp"
	"strings"
	"time"
)

const APIVersion = "commerce.platform.example.com/v2"

var digest = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

type Money struct {
	Currency  string `json:"currency"`
	MinorUnit int64  `json:"minor_unit"`
}

func (m Money) Validate() error {
	if !regexp.MustCompile(`^[A-Z]{3}$`).MatchString(m.Currency) || m.MinorUnit < 0 {
		return errors.New("invalid money")
	}
	return nil
}

type PriceSnapshot struct {
	SnapshotID string    `json:"snapshot_id"`
	Provider   string    `json:"provider"`
	Version    string    `json:"version"`
	Digest     string    `json:"digest"`
	ObservedAt time.Time `json:"observed_at"`
}

type RateCard struct {
	RateCardID        string `json:"rate_card_id"`
	Version           string `json:"version"`
	MarkupBasisPoints int64  `json:"markup_basis_points"`
	Currency          string `json:"currency"`
	PriceSnapshotID   string `json:"price_snapshot_id"`
}

func (r RateCard) Validate() error {
	if r.RateCardID == "" || r.Version == "" || r.PriceSnapshotID == "" || r.MarkupBasisPoints < 1000 || r.MarkupBasisPoints > 5000 || !regexp.MustCompile(`^[A-Z]{3}$`).MatchString(r.Currency) {
		return errors.New("invalid beta rate card")
	}
	return nil
}

type EstimateLine struct {
	Meter        string `json:"meter"`
	Quantity     int64  `json:"quantity"`
	Unit         string `json:"unit"`
	ProviderCost Money  `json:"provider_cost"`
	CustomerCost Money  `json:"customer_cost"`
	PriceKnown   bool   `json:"price_known"`
}

// RequestedRuntimeAllocation is the value-free, normalized capacity requested
// by rendered Kubernetes resources. Pricing is deliberately kept in the
// immutable rate card so the same allocation can be re-estimated without
// re-rendering a release.
type RequestedRuntimeAllocation struct {
	CPUMillicores int64 `json:"cpu_millicores"`
	MemoryMiB     int64 `json:"memory_mib"`
	StorageMiB    int64 `json:"storage_mib"`
	LoadBalancers int64 `json:"load_balancers"`
}

func (a RequestedRuntimeAllocation) Validate() error {
	if a.CPUMillicores < 0 || a.MemoryMiB < 0 || a.StorageMiB < 0 || a.LoadBalancers < 0 {
		return errors.New("invalid requested runtime allocation")
	}
	return nil
}

type CostEstimate struct {
	EstimateID        string         `json:"estimate_id"`
	Version           string         `json:"version"`
	PlanHash          string         `json:"plan_hash"`
	RateCardID        string         `json:"rate_card_id"`
	RateCardVersion   string         `json:"rate_card_version"`
	PriceSnapshotID   string         `json:"price_snapshot_id"`
	MarkupBasisPoints int64          `json:"markup_basis_points"`
	Lines             []EstimateLine `json:"lines"`
	Minimum           Money          `json:"minimum"`
	Maximum           Money          `json:"maximum"`
	ApprovalRequired  bool           `json:"approval_required"`
	ExpiresAt         time.Time      `json:"expires_at"`
}

func (e CostEstimate) Validate(now time.Time) error {
	if e.EstimateID == "" || e.Version == "" || !digest.MatchString(e.PlanHash) || e.RateCardID == "" || e.RateCardVersion == "" || e.PriceSnapshotID == "" || e.MarkupBasisPoints < 1000 || e.MarkupBasisPoints > 5000 || len(e.Lines) == 0 || !e.ExpiresAt.After(now) || e.Minimum.Validate() != nil || e.Maximum.Validate() != nil || e.Minimum.Currency != e.Maximum.Currency || e.Minimum.MinorUnit > e.Maximum.MinorUnit {
		return errors.New("invalid cost estimate")
	}
	for _, line := range e.Lines {
		if strings.TrimSpace(line.Meter) == "" || line.Quantity < 0 || line.ProviderCost.Validate() != nil || line.CustomerCost.Validate() != nil || line.ProviderCost.Currency != e.Minimum.Currency || line.CustomerCost.Currency != e.Minimum.Currency {
			return errors.New("invalid estimate line")
		}
		if !line.PriceKnown && !e.ApprovalRequired {
			return errors.New("unknown price requires approval")
		}
	}
	return nil
}

type ExecutionReservation struct {
	ReservationID   string    `json:"reservation_id"`
	ProjectID       string    `json:"project_id"`
	EstimateID      string    `json:"estimate_id"`
	EstimateVersion string    `json:"estimate_version"`
	PlanHash        string    `json:"plan_hash"`
	Maximum         Money     `json:"maximum"`
	ExpiresAt       time.Time `json:"expires_at"`
}

type UsageFact struct {
	UsageID          string    `json:"usage_id"`
	ProjectID        string    `json:"project_id"`
	OperationID      string    `json:"operation_id"`
	Provider         string    `json:"provider"`
	ProviderEventID  string    `json:"provider_event_id"`
	Meter            string    `json:"meter"`
	Quantity         int64     `json:"quantity"`
	DeduplicationKey string    `json:"deduplication_key"`
	OccurredAt       time.Time `json:"occurred_at"`
}

func (f UsageFact) Validate() error {
	if strings.TrimSpace(f.UsageID) == "" || strings.TrimSpace(f.ProjectID) == "" || strings.TrimSpace(f.OperationID) == "" || strings.TrimSpace(f.Provider) == "" || strings.TrimSpace(f.ProviderEventID) == "" || strings.TrimSpace(f.Meter) == "" || f.Quantity < 0 || strings.TrimSpace(f.DeduplicationKey) == "" || f.OccurredAt.IsZero() {
		return errors.New("invalid provider usage fact")
	}
	return nil
}

type SettlementState string

const (
	SettlementApplied            SettlementState = "APPLIED"
	SettlementPartialRecoverable SettlementState = "PARTIAL_APPLY_RECOVERABLE"
)

type ReservationSettlement struct {
	SettlementID          string          `json:"settlement_id"`
	ReservationID         string          `json:"reservation_id"`
	ProjectID             string          `json:"project_id"`
	Resource              string          `json:"resource"`
	OperationID           string          `json:"operation_id"`
	SettledQuantity       int64           `json:"settled_quantity"`
	ReleasedQuantity      int64           `json:"released_quantity"`
	ObservedResourceCount int64           `json:"observed_resource_count"`
	State                 SettlementState `json:"state"`
	Recoverable           bool            `json:"recoverable"`
	SettledAt             time.Time       `json:"settled_at"`
}

func (s ReservationSettlement) Validate() error {
	if strings.TrimSpace(s.SettlementID) == "" || strings.TrimSpace(s.ReservationID) == "" || strings.TrimSpace(s.ProjectID) == "" || strings.TrimSpace(s.Resource) == "" || strings.TrimSpace(s.OperationID) == "" || s.SettledQuantity < 0 || s.ReleasedQuantity < 0 || s.ObservedResourceCount < 0 || s.SettledAt.IsZero() {
		return errors.New("invalid reservation settlement")
	}
	switch s.State {
	case SettlementApplied:
		if s.Recoverable || s.ReleasedQuantity != 0 {
			return errors.New("invalid applied reservation settlement")
		}
	case SettlementPartialRecoverable:
		if !s.Recoverable || s.ReleasedQuantity == 0 {
			return errors.New("invalid partial reservation settlement")
		}
	default:
		return errors.New("invalid reservation settlement state")
	}
	return nil
}
