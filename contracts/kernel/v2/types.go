// Package v2 defines provider-neutral execution-kernel contracts for project-scoped work.
package v2

import (
	"errors"
	"regexp"
	"strings"
	"time"
)

const APIVersion = "kernel.platform.example.com/v2"

var (
	safeID       = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,62}$`)
	digestSHA256 = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
)

type PrincipalKind string

const (
	PrincipalUser      PrincipalKind = "USER"
	PrincipalAgent     PrincipalKind = "AGENT"
	PrincipalWorkspace PrincipalKind = "WORKSPACE"
	PrincipalService   PrincipalKind = "SERVICE"
)

type Principal struct {
	Kind        PrincipalKind `json:"kind"`
	SubjectID   string        `json:"subject_id"`
	TenantID    string        `json:"tenant_id"`
	ProjectID   string        `json:"project_id"`
	WorkspaceID string        `json:"workspace_id,omitempty"`
	TaskID      string        `json:"task_id,omitempty"`
}

func (p Principal) Validate() error {
	if !validPrincipalKind(p.Kind) || !safeID.MatchString(p.SubjectID) || !safeID.MatchString(p.TenantID) || !safeID.MatchString(p.ProjectID) {
		return errors.New("invalid principal")
	}
	if p.WorkspaceID != "" && !safeID.MatchString(p.WorkspaceID) {
		return errors.New("invalid workspace scope")
	}
	if p.TaskID != "" && !safeID.MatchString(p.TaskID) {
		return errors.New("invalid task scope")
	}
	if p.Kind == PrincipalWorkspace && p.WorkspaceID == "" {
		return errors.New("workspace principal requires workspace scope")
	}
	return nil
}

func validPrincipalKind(v PrincipalKind) bool {
	switch v {
	case PrincipalUser, PrincipalAgent, PrincipalWorkspace, PrincipalService:
		return true
	default:
		return false
	}
}

type OperationState string

const (
	OperationPending           OperationState = "PENDING"
	OperationRunning           OperationState = "RUNNING"
	OperationWaitingApproval   OperationState = "WAITING_APPROVAL"
	OperationWaitingDependency OperationState = "WAITING_DEPENDENCY"
	OperationSucceeded         OperationState = "SUCCEEDED"
	OperationFailed            OperationState = "FAILED"
	OperationCanceled          OperationState = "CANCELED"
)

func (s OperationState) Terminal() bool {
	return s == OperationSucceeded || s == OperationFailed || s == OperationCanceled
}

func (s OperationState) Valid() bool {
	switch s {
	case OperationPending, OperationRunning, OperationWaitingApproval, OperationWaitingDependency, OperationSucceeded, OperationFailed, OperationCanceled:
		return true
	default:
		return false
	}
}

type OperationRef struct {
	OperationID string         `json:"operation_id"`
	TenantID    string         `json:"tenant_id"`
	ProjectID   string         `json:"project_id"`
	State       OperationState `json:"state"`
}

func (r OperationRef) Validate() error {
	if !safeID.MatchString(r.OperationID) || !safeID.MatchString(r.TenantID) || !safeID.MatchString(r.ProjectID) || !r.State.Valid() {
		return errors.New("invalid operation reference")
	}
	return nil
}

type OperationNode struct {
	NodeID       string         `json:"node_id"`
	Kind         string         `json:"kind"`
	State        OperationState `json:"state"`
	DependsOn    []string       `json:"depends_on,omitempty"`
	CheckpointID string         `json:"checkpoint_id,omitempty"`
}

type OperationGraph struct {
	OperationID string          `json:"operation_id"`
	Version     int64           `json:"version"`
	Nodes       []OperationNode `json:"nodes"`
	CanceledAt  *time.Time      `json:"canceled_at,omitempty"`
}

func (g OperationGraph) Validate() error {
	if !safeID.MatchString(g.OperationID) || g.Version < 1 || len(g.Nodes) == 0 || len(g.Nodes) > 256 {
		return errors.New("invalid operation graph")
	}
	seen := make(map[string]struct{}, len(g.Nodes))
	for _, node := range g.Nodes {
		if !safeID.MatchString(node.NodeID) || strings.TrimSpace(node.Kind) == "" || !node.State.Valid() {
			return errors.New("invalid operation node")
		}
		if _, ok := seen[node.NodeID]; ok {
			return errors.New("duplicate operation node")
		}
		seen[node.NodeID] = struct{}{}
	}
	for _, node := range g.Nodes {
		for _, dependency := range node.DependsOn {
			if dependency == node.NodeID {
				return errors.New("operation node depends on itself")
			}
			if _, ok := seen[dependency]; !ok {
				return errors.New("unknown operation dependency")
			}
		}
	}
	return nil
}

type Checkpoint struct {
	CheckpointID string           `json:"checkpoint_id"`
	OperationID  string           `json:"operation_id"`
	NodeID       string           `json:"node_id"`
	Version      int64            `json:"version"`
	Payload      []byte           `json:"payload,omitempty"`
	RecordedAt   time.Time        `json:"recorded_at"`
	State        OperationState   `json:"state"`
	Lease        *CredentialLease `json:"credential_lease,omitempty"`
}

type CredentialLease struct {
	LeaseID      string    `json:"lease_id"`
	Kind         string    `json:"kind"`
	Audience     string    `json:"audience"`
	ExpiresAt    time.Time `json:"expires_at"`
	Renewable    bool      `json:"renewable"`
	RevocationID string    `json:"revocation_id"`
}

func (l CredentialLease) Validate(now time.Time) error {
	if !safeID.MatchString(l.LeaseID) || strings.TrimSpace(l.Kind) == "" || strings.TrimSpace(l.Audience) == "" || !safeID.MatchString(l.RevocationID) || !l.ExpiresAt.After(now) {
		return errors.New("invalid credential lease")
	}
	return nil
}

type ApprovalBinding struct {
	ApprovalGrantID string    `json:"approval_grant_id"`
	PlanHash        string    `json:"plan_hash"`
	EstimateVersion string    `json:"estimate_version"`
	Target          string    `json:"target"`
	ActorID         string    `json:"actor_id"`
	ExpiresAt       time.Time `json:"expires_at"`
}

func (b ApprovalBinding) Validate(now time.Time) error {
	if !safeID.MatchString(b.ApprovalGrantID) || !digestSHA256.MatchString(b.PlanHash) || !safeID.MatchString(b.EstimateVersion) || strings.TrimSpace(b.Target) == "" || !safeID.MatchString(b.ActorID) || !b.ExpiresAt.After(now) {
		return errors.New("invalid approval binding")
	}
	return nil
}

type PublicError struct {
	Code        string         `json:"code"`
	Message     string         `json:"message"`
	OperationID string         `json:"operation_id,omitempty"`
	Retryable   bool           `json:"retryable"`
	Details     map[string]any `json:"details,omitempty"`
}
