// Package v1 defines OpenTofu planning, state and apply contracts.
package v1

import (
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
	Changes             []ResourceChange `json:"changes"`
	Destructive         bool             `json:"destructive"`
	RequiresApproval    bool             `json:"requires_approval"`
	EstimateVersion     string           `json:"estimate_version"`
	EstimateFingerprint string           `json:"estimate_fingerprint"`
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
