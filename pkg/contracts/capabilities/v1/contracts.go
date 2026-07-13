// Package v1 defines project-scoped governed capability gateway contracts.
package v1

import (
	"errors"
	"strings"
	"time"
)

const APIVersion = "capabilities.platform.example.com/v1"

type CapabilityKind string

const (
	CapabilityLLM        CapabilityKind = "llm.openrouter-compatible"
	CapabilityApify      CapabilityKind = "parser.apify"
	CapabilityBrightData CapabilityKind = "parser.bright-data"
)

type Binding struct {
	BindingID       string         `json:"binding_id"`
	ProjectID       string         `json:"project_id"`
	Kind            CapabilityKind `json:"kind"`
	Endpoint        string         `json:"endpoint"`
	ProjectTokenRef string         `json:"project_token_ref"`
	BudgetID        string         `json:"budget_id"`
	RateLimitID     string         `json:"rate_limit_id"`
	ProviderPolicy  string         `json:"provider_policy"`
}

func (b Binding) Validate() error {
	if b.BindingID == "" || b.ProjectID == "" || b.Endpoint == "" || b.ProjectTokenRef == "" || b.BudgetID == "" || b.RateLimitID == "" || b.ProviderPolicy == "" {
		return errors.New("invalid capability binding")
	}
	switch b.Kind {
	case CapabilityLLM, CapabilityApify, CapabilityBrightData:
		return nil
	default:
		return errors.New("unsupported capability")
	}
}

type UsageFact struct {
	UsageID           string         `json:"usage_id"`
	BindingID         string         `json:"binding_id"`
	Kind              CapabilityKind `json:"kind"`
	Provider          string         `json:"provider"`
	ProviderRequestID string         `json:"provider_request_id"`
	DeduplicationKey  string         `json:"deduplication_key"`
	InputUnits        int64          `json:"input_units"`
	OutputUnits       int64          `json:"output_units"`
	OccurredAt        time.Time      `json:"occurred_at"`
}

func (f UsageFact) Validate() error {
	if strings.TrimSpace(f.UsageID) == "" || strings.TrimSpace(f.BindingID) == "" || strings.TrimSpace(f.Provider) == "" || strings.TrimSpace(f.DeduplicationKey) == "" || f.InputUnits < 0 || f.OutputUnits < 0 || f.OccurredAt.IsZero() {
		return errors.New("invalid capability usage fact")
	}
	return nil
}
