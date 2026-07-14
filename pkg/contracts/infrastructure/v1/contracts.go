// Package v1 defines OpenTofu planning, state and apply contracts.
package v1

import (
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"
)

const APIVersion = "infrastructure.platform.example.com/v1"

var digest = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

type ChangeAction string

const (
	ActionCreate  ChangeAction = "CREATE"
	ActionUpdate  ChangeAction = "UPDATE"
	ActionReplace ChangeAction = "REPLACE"
	ActionDelete  ChangeAction = "DELETE"
	ActionNoOp    ChangeAction = "NO_OP"
)

type ResourceChange struct {
	Address      string       `json:"address"`
	Provider     string       `json:"provider"`
	ResourceType string       `json:"resource_type"`
	Action       ChangeAction `json:"action"`
	ExternalID   string       `json:"external_id,omitempty"`
}

// RetainedResource is an explicit non-deletion decision from platform.yaml/v2.
// It is hashed with the executable plan and shown on the approval surface so
// omission from OpenTofu's delete set cannot be confused with a parser bug.
type RetainedResource struct {
	Address      string `json:"address"`
	Provider     string `json:"provider"`
	ResourceType string `json:"resource_type"`
	ExternalID   string `json:"external_id,omitempty"`
	Policy       string `json:"policy"`
	Reason       string `json:"reason"`
}

func (r RetainedResource) Validate() error { return validateRetainedResource(r) }

type PlanRef struct {
	PlanID          string    `json:"plan_id"`
	ProjectID       string    `json:"project_id"`
	WorkspaceID     string    `json:"workspace_id"`
	SourceSHA       string    `json:"source_sha"`
	PlanHash        string    `json:"plan_hash"`
	StateGeneration int64     `json:"state_generation"`
	CreatedAt       time.Time `json:"created_at"`
}

func (r PlanRef) Validate() error {
	if strings.TrimSpace(r.PlanID) == "" || strings.TrimSpace(r.ProjectID) == "" || strings.TrimSpace(r.WorkspaceID) == "" || !digest.MatchString(r.PlanHash) || r.StateGeneration < 0 || r.CreatedAt.IsZero() {
		return errors.New("invalid infrastructure plan reference")
	}
	return nil
}

type PlanSummary struct {
	PlanRef
	Changes             []ResourceChange   `json:"changes"`
	RetainedResources   []RetainedResource `json:"retained_resources,omitempty"`
	Destructive         bool               `json:"destructive"`
	RequiresApproval    bool               `json:"requires_approval"`
	EstimateVersion     string             `json:"estimate_version"`
	EstimateFingerprint string             `json:"estimate_fingerprint"`
}

// ChangeCounts is a value-only summary suitable for human approval surfaces.
// Resource details remain available separately in the plan artifact.
type ChangeCounts struct {
	Create  int `json:"create"`
	Update  int `json:"update"`
	Replace int `json:"replace"`
	Delete  int `json:"delete"`
	NoOp    int `json:"no_op"`
	Retain  int `json:"retain,omitempty"`
}

// CostDeltaRange is signed: deletion may reduce the monthly amount. Complete
// is false whenever the plan lacks enough provider pricing or before/after
// detail to calculate an exact delta.
type CostDeltaRange struct {
	Currency     string `json:"currency"`
	MinimumMinor int64  `json:"minimum_minor"`
	MaximumMinor int64  `json:"maximum_minor"`
	Complete     bool   `json:"complete"`
}

type DestructionRisk struct {
	Address string       `json:"address"`
	Action  ChangeAction `json:"action"`
	Reason  string       `json:"reason"`
}

// ApprovalSummary is the additive v1 payload shown before an exact-plan grant.
// It intentionally contains no raw provider values or credentials.
type ApprovalSummary struct {
	PlanID            string             `json:"plan_id"`
	ProjectID         string             `json:"project_id"`
	Target            string             `json:"target"`
	PlanHash          string             `json:"plan_hash"`
	Counts            ChangeCounts       `json:"counts"`
	DestructionRisks  []DestructionRisk  `json:"destruction_risks"`
	RetainedResources []RetainedResource `json:"retained_resources,omitempty"`
	MonthlyDelta      CostDeltaRange     `json:"monthly_delta"`
	OneTimeDelta      CostDeltaRange     `json:"one_time_delta"`
	Unknowns          []string           `json:"unknowns"`
	EstimateVersion   string             `json:"estimate_version"`
	ReservationID     string             `json:"reservation_id"`
	ApprovalExpiresAt time.Time          `json:"approval_expires_at"`
}

func (s ApprovalSummary) Validate() error {
	if strings.TrimSpace(s.PlanID) == "" || strings.TrimSpace(s.ProjectID) == "" || strings.TrimSpace(s.Target) == "" ||
		!digest.MatchString(s.PlanHash) || strings.TrimSpace(s.EstimateVersion) == "" || strings.TrimSpace(s.ReservationID) == "" ||
		s.ApprovalExpiresAt.IsZero() || validateDelta(s.MonthlyDelta) != nil || validateDelta(s.OneTimeDelta) != nil ||
		s.Counts.Create < 0 || s.Counts.Update < 0 || s.Counts.Replace < 0 || s.Counts.Delete < 0 || s.Counts.NoOp < 0 ||
		s.Counts.Retain < 0 || s.Counts.Retain != len(s.RetainedResources) {
		return errors.New("invalid approval summary")
	}
	for _, risk := range s.DestructionRisks {
		if strings.TrimSpace(risk.Address) == "" || strings.TrimSpace(risk.Reason) == "" || (risk.Action != ActionDelete && risk.Action != ActionReplace) {
			return errors.New("invalid approval destruction risk")
		}
	}
	for _, retained := range s.RetainedResources {
		if retained.Validate() != nil {
			return errors.New("invalid retained resource")
		}
	}
	return nil
}

func validateRetainedResource(resource RetainedResource) error {
	address := strings.TrimSpace(resource.Address)
	provider := strings.TrimSpace(resource.Provider)
	resourceType := strings.TrimSpace(resource.ResourceType)
	externalID := strings.TrimSpace(resource.ExternalID)
	policy := strings.TrimSpace(resource.Policy)
	reason := strings.TrimSpace(resource.Reason)
	if address == "" || len(address) > 512 || provider == "" || len(provider) > 512 ||
		resourceType == "" || len(resourceType) > 256 || len(externalID) > 512 ||
		policy != "platform.yaml/v2:retain" || len(policy) > 128 || reason == "" || len(reason) > 1024 ||
		strings.ContainsRune(address, '\x00') || strings.ContainsRune(provider, '\x00') ||
		strings.ContainsRune(resourceType, '\x00') || strings.ContainsRune(externalID, '\x00') ||
		strings.ContainsRune(policy, '\x00') || strings.ContainsRune(reason, '\x00') {
		return errors.New("invalid retained resource")
	}
	return nil
}

func validateDelta(delta CostDeltaRange) error {
	if !regexp.MustCompile(`^[A-Z]{3}$`).MatchString(delta.Currency) || delta.MinimumMinor > delta.MaximumMinor {
		return errors.New("invalid cost delta")
	}
	return nil
}

type ApplyAuthorization struct {
	PlanID          string    `json:"plan_id"`
	PlanHash        string    `json:"plan_hash"`
	EstimateVersion string    `json:"estimate_version"`
	ReservationID   string    `json:"reservation_id"`
	ApprovalGrantID string    `json:"approval_grant_id,omitempty"`
	Target          string    `json:"target"`
	ActorID         string    `json:"actor_id"`
	ExpiresAt       time.Time `json:"expires_at"`
}

// AgentPlanReceipt is accepted only over the workspace agent's authenticated
// outbound mTLS session. PlanJSON contains a value-free normalized change list,
// never the full OpenTofu before/after values.
type AgentPlanReceipt struct {
	SessionID          string             `json:"session_id"`
	ExecutionSessionID string             `json:"execution_session_id"`
	CommandID          string             `json:"command_id"`
	ArtifactDigest     string             `json:"artifact_digest"`
	PlanJSON           json.RawMessage    `json:"plan_json"`
	RetainedResources  []RetainedResource `json:"retained_resources,omitempty"`
	CapturedAt         time.Time          `json:"captured_at"`
}

func (r AgentPlanReceipt) Validate() error {
	if strings.TrimSpace(r.SessionID) == "" || strings.TrimSpace(r.ExecutionSessionID) == "" || strings.TrimSpace(r.CommandID) == "" || !digest.MatchString(r.ArtifactDigest) || len(r.PlanJSON) == 0 || len(r.PlanJSON) > 8<<20 || !json.Valid(r.PlanJSON) || r.CapturedAt.IsZero() || len(r.RetainedResources) > 1024 {
		return errors.New("invalid authenticated plan receipt")
	}
	seen := make(map[string]struct{}, len(r.RetainedResources))
	for _, retained := range r.RetainedResources {
		if retained.Validate() != nil {
			return errors.New("invalid authenticated plan receipt")
		}
		address := strings.TrimSpace(retained.Address)
		if _, exists := seen[address]; exists {
			return errors.New("invalid authenticated plan receipt")
		}
		seen[address] = struct{}{}
	}
	return nil
}

func (a ApplyAuthorization) Validate(now time.Time, approvalRequired bool) error {
	if strings.TrimSpace(a.PlanID) == "" || !digest.MatchString(a.PlanHash) || strings.TrimSpace(a.EstimateVersion) == "" || strings.TrimSpace(a.ReservationID) == "" || strings.TrimSpace(a.Target) == "" || strings.TrimSpace(a.ActorID) == "" || !a.ExpiresAt.After(now) {
		return errors.New("invalid apply authorization")
	}
	if approvalRequired && a.ApprovalGrantID == "" {
		return errors.New("approval grant is required")
	}
	return nil
}

type StateRef struct {
	StateID         string `json:"state_id"`
	ProjectID       string `json:"project_id"`
	Generation      int64  `json:"generation"`
	BlobDigest      string `json:"blob_digest"`
	EncryptionKeyID string `json:"encryption_key_id"`
	LockOwner       string `json:"lock_owner,omitempty"`
}

func (r StateRef) Validate() error {
	if r.StateID == "" || r.ProjectID == "" || r.Generation < 0 || !digest.MatchString(r.BlobDigest) || r.EncryptionKeyID == "" {
		return errors.New("invalid state reference")
	}
	return nil
}
