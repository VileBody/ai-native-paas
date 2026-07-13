// Package v1 contains the frozen public contracts exported by Application Attachments.
// Values and provider credentials intentionally never cross this boundary.
package v1

import (
	"context"
	"errors"
	"regexp"
	"sort"
	"strings"
	"time"
)

type SecretScope string

const (
	SecretScopeRuntime SecretScope = "runtime"
	SecretScopeBuild   SecretScope = "build"
)

type SecretPhase string

const (
	SecretPhaseRuntime SecretPhase = "runtime"
	SecretPhaseDetect  SecretPhase = "detect"
	SecretPhaseBuild   SecretPhase = "build"
)

type SecretMetadata struct {
	Name      string      `json:"name"`
	Scope     SecretScope `json:"scope,omitempty"`
	Phase     SecretPhase `json:"phase,omitempty"`
	Version   int64       `json:"version"`
	ExpiresAt time.Time   `json:"expires_at,omitempty"`
	UpdatedAt time.Time   `json:"updated_at"`
	Deleted   bool        `json:"deleted,omitempty"`
}
type ServiceType string

const (
	ServicePostgreSQL ServiceType = "postgresql"
	ServiceRedis      ServiceType = "redis"
	ServiceS3         ServiceType = "s3"
)

type ServiceState string

const (
	ServiceRequested       ServiceState = "REQUESTED"
	ServiceProvisioning    ServiceState = "PROVISIONING"
	ServiceReady           ServiceState = "READY"
	ServiceFailedRetryable ServiceState = "FAILED_RETRYABLE"
	ServiceFailedFinal     ServiceState = "FAILED_FINAL"
	ServiceDeleting        ServiceState = "DELETING"
	ServiceRetainedBackup  ServiceState = "RETAINED_BACKUP"
	ServiceDeleted         ServiceState = "DELETED"
)

type BindingState string

const (
	BindingRequested BindingState = "REQUESTED"
	BindingActive    BindingState = "ACTIVE"
	BindingRotating  BindingState = "ROTATING"
	BindingRevoking  BindingState = "REVOKING"
	BindingRevoked   BindingState = "REVOKED"
)

type DomainState string

const (
	DomainAwaitingVerification DomainState = "AWAITING_VERIFICATION"
	DomainVerified             DomainState = "VERIFIED"
	DomainTLSPending           DomainState = "TLS_PENDING"
	DomainActive               DomainState = "ACTIVE"
	DomainVerificationFailed   DomainState = "VERIFICATION_FAILED"
	DomainQuarantined          DomainState = "QUARANTINED"
	DomainReleased             DomainState = "RELEASED"
)

type ServiceInstanceRef struct {
	ServiceInstanceID string      `json:"service_instance_id,omitempty"`
	InstanceID        string      `json:"instance_id,omitempty"`
	TenantID          string      `json:"tenant_id,omitempty"`
	PlanID            string      `json:"plan_id,omitempty"`
	Type              ServiceType `json:"type,omitempty"`
	State             string      `json:"state"`
}
type BindingRef struct {
	BindingID  string `json:"binding_id"`
	SnapshotID string `json:"snapshot_id"`
	State      string `json:"state"`
}
type ServiceBindingRef struct {
	BindingID     string       `json:"binding_id"`
	InstanceID    string       `json:"instance_id"`
	EnvironmentID string       `json:"environment_id"`
	State         BindingState `json:"state"`
}
type DomainRef struct {
	DomainClaimID string `json:"domain_claim_id"`
	Hostname      string `json:"hostname"`
	State         string `json:"state"`
}
type DomainClaimRef struct {
	ClaimID       string      `json:"claim_id"`
	Hostname      string      `json:"hostname"`
	EnvironmentID string      `json:"environment_id"`
	State         DomainState `json:"state"`
}

type AttachmentSnapshot struct {
	SnapshotID      string    `json:"snapshot_id"`
	TenantID        string    `json:"tenant_id"`
	ApplicationID   string    `json:"application_id"`
	EnvironmentID   string    `json:"environment_id"`
	Version         int64     `json:"version"`
	SecretSetRef    string    `json:"secret_set_ref"`
	ServiceBindings []string  `json:"service_bindings,omitempty"`
	ActiveDomains   []string  `json:"active_domains,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
}
type AttachmentSnapshotRef struct {
	SnapshotID    string `json:"snapshot_id"`
	EnvironmentID string `json:"environment_id"`
	Version       int64  `json:"version"`
}
type AttachmentResolver interface {
	Resolve(context.Context, string, string) (AttachmentSnapshot, error)
}

// Agent-facing zero-trust facade. Value is write-only and must be discarded at the boundary.
type SetSecretRequest struct {
	TenantID       string `json:"tenant_id"`
	ApplicationID  string `json:"application_id"`
	EnvironmentID  string `json:"environment_id"`
	Name           string `json:"name"`
	Value          string `json:"value"`
	IdempotencyKey string `json:"idempotency_key"`
	ActorID        string `json:"actor_id"`
}
type ServiceRequest struct {
	TenantID       string `json:"tenant_id"`
	ServiceType    string `json:"service_type"`
	Plan           string `json:"plan"`
	Name           string `json:"name"`
	IdempotencyKey string `json:"idempotency_key"`
	ActorID        string `json:"actor_id"`
}
type BindRequest struct {
	TenantID          string `json:"tenant_id"`
	ServiceInstanceID string `json:"service_instance_id"`
	ApplicationID     string `json:"application_id"`
	EnvironmentID     string `json:"environment_id"`
	IdempotencyKey    string `json:"idempotency_key"`
	ActorID           string `json:"actor_id"`
}
type DomainRequest struct {
	TenantID       string `json:"tenant_id"`
	ApplicationID  string `json:"application_id"`
	EnvironmentID  string `json:"environment_id"`
	Hostname       string `json:"hostname"`
	IdempotencyKey string `json:"idempotency_key"`
	ActorID        string `json:"actor_id"`
}
type Service interface {
	SetSecret(context.Context, SetSecretRequest) (SecretMetadata, error)
	ListSecretMetadata(context.Context, string, string, string) ([]SecretMetadata, error)
	Provision(context.Context, ServiceRequest) (ServiceInstanceRef, error)
	Bind(context.Context, BindRequest) (BindingRef, error)
	AddDomain(context.Context, DomainRequest) (DomainRef, error)
}

var idRE = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._:-]{0,127}$`)
var secretRE = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,127}$`)

func (m SecretMetadata) Validate() error {
	if !secretRE.MatchString(m.Name) || m.Version < 1 || m.UpdatedAt.IsZero() {
		return errors.New("invalid secret metadata")
	}
	if m.Scope != "" {
		if m.Scope != SecretScopeRuntime && m.Scope != SecretScopeBuild {
			return errors.New("invalid secret scope")
		}
		if m.Scope == SecretScopeBuild && m.ExpiresAt.IsZero() {
			return errors.New("build secret expiry required")
		}
	}
	return nil
}
func (s AttachmentSnapshot) Validate() error {
	for _, v := range []string{s.SnapshotID, s.TenantID, s.ApplicationID, s.EnvironmentID, s.SecretSetRef} {
		if !idRE.MatchString(strings.TrimSpace(v)) {
			return errors.New("invalid attachment snapshot")
		}
	}
	if s.Version < 1 || s.CreatedAt.IsZero() {
		return errors.New("invalid attachment snapshot")
	}
	if !safeList(s.ServiceBindings) || !safeList(s.ActiveDomains) {
		return errors.New("invalid attachment snapshot list")
	}
	return nil
}
func safeList(v []string) bool {
	if !sort.StringsAreSorted(v) {
		return false
	}
	last := ""
	for _, x := range v {
		if !idRE.MatchString(x) || x == last {
			return false
		}
		last = x
	}
	return true
}
