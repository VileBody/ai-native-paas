// Package domain owns the provider-neutral Application Attachments model.
package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	attachmentsv1 "github.com/keir-research/ai-native-paas/pkg/contracts/attachments/v1"
)

type Code string

const (
	CodeInvalidArgument   Code = "INVALID_ARGUMENT"
	CodeForbidden         Code = "FORBIDDEN"
	CodeNotFound          Code = "NOT_FOUND"
	CodeConflict          Code = "CONFLICT"
	CodeStaleVersion      Code = "STALE_VERSION"
	CodeApprovalRequired  Code = "APPROVAL_REQUIRED"
	CodeEntitlementDenied Code = "ENTITLEMENT_DENIED"
	CodeRetryable         Code = "RETRYABLE"
	CodeUnavailable       Code = "UNAVAILABLE"
	CodeInternal          Code = "INTERNAL"
)

type Error struct {
	Code    Code
	Message string
	Cause   error
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	if e.Cause != nil {
		return fmt.Sprintf("%s: %s: %v", e.Code, e.Message, e.Cause)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

func (e *Error) Unwrap() error { return e.Cause }

func NewError(code Code, message string) error {
	return &Error{Code: code, Message: message}
}

func Wrap(code Code, message string, cause error) error {
	if cause == nil {
		return NewError(code, message)
	}
	var typed *Error
	if errors.As(cause, &typed) && typed.Code == code && typed.Message == message {
		return cause
	}
	return &Error{Code: code, Message: message, Cause: cause}
}

func Hash(value any) string {
	raw, _ := json.Marshal(value)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func CanonicalStrings(values []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

var secretNameRE = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,127}$`)
var dnsLabelRE = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)

var reservedSecretNames = map[string]struct{}{
	"DATABASE_URL": {}, "PORT": {}, "HOST": {}, "HOSTNAME": {}, "KUBERNETES_SERVICE_HOST": {},
}

func ValidSecretName(value string) bool {
	if !secretNameRE.MatchString(value) || strings.HasPrefix(value, "PLATFORM_") {
		return false
	}
	_, reserved := reservedSecretNames[value]
	return !reserved
}

func ValidDNSLabel(value string) bool {
	return dnsLabelRE.MatchString(strings.ToLower(strings.TrimSpace(value)))
}

func CanonicalHostname(value string) (string, error) {
	value = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(value)), ".")
	if value == "" || len(value) > 253 || strings.ContainsAny(value, "/:@ \\") {
		return "", NewError(CodeInvalidArgument, "invalid hostname")
	}
	labels := strings.Split(value, ".")
	if len(labels) < 2 {
		return "", NewError(CodeInvalidArgument, "hostname must be fully qualified")
	}
	for _, label := range labels {
		if !ValidDNSLabel(label) {
			return "", NewError(CodeInvalidArgument, "invalid hostname")
		}
	}
	return value, nil
}

func DNSHash(values []string) string {
	canonical := make([]string, 0, len(values))
	for _, value := range values {
		canonical = append(canonical, strings.TrimSpace(value))
	}
	sort.Strings(canonical)
	return Hash(canonical)
}

type SecretSet struct {
	ID            string    `json:"id"`
	TenantID      string    `json:"tenant_id"`
	ApplicationID string    `json:"application_id"`
	EnvironmentID string    `json:"environment_id"`
	ProviderPath  string    `json:"provider_path"`
	Version       int64     `json:"version"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

type SecretMetadata struct {
	ID              string                    `json:"id"`
	TenantID        string                    `json:"tenant_id"`
	SecretSetID     string                    `json:"secret_set_id"`
	ApplicationID   string                    `json:"application_id"`
	EnvironmentID   string                    `json:"environment_id"`
	Name            string                    `json:"name"`
	Scope           attachmentsv1.SecretScope `json:"scope"`
	Phase           attachmentsv1.SecretPhase `json:"phase"`
	ProviderRef     string                    `json:"provider_ref"`
	ProviderVersion string                    `json:"provider_version"`
	Version         int64                     `json:"version"`
	ExpiresAt       time.Time                 `json:"expires_at,omitempty"`
	Deleted         bool                      `json:"deleted,omitempty"`
	CreatedAt       time.Time                 `json:"created_at"`
	UpdatedAt       time.Time                 `json:"updated_at"`
}

func (m SecretMetadata) Public() attachmentsv1.SecretMetadata {
	return attachmentsv1.SecretMetadata{Name: m.Name, Scope: m.Scope, Phase: m.Phase, Version: m.Version, ExpiresAt: m.ExpiresAt, UpdatedAt: m.UpdatedAt, Deleted: m.Deleted}
}

type ServicePlan struct {
	ID                     string                    `json:"id"`
	Version                int64                     `json:"version"`
	Type                   attachmentsv1.ServiceType `json:"type"`
	Provider               string                    `json:"provider"`
	ProviderPlan           string                    `json:"provider_plan"`
	ProviderMappingVersion string                    `json:"provider_mapping_version"`
	Dedicated              bool                      `json:"dedicated"`
	Capabilities           []string                  `json:"capabilities"`
	Enabled                bool                      `json:"enabled"`
	BackupBeforePurge      bool                      `json:"backup_before_purge"`
	CreatedAt              time.Time                 `json:"created_at"`
}

// ProviderResourceDestroyPlan is the value-only, canonical intent that must be
// approved before a managed provider resource can be destroyed. It binds the
// approval to both the external target and the immutable provider mapping.
type ProviderResourceDestroyPlan struct {
	Action                 string                    `json:"action"`
	TenantID               string                    `json:"tenant_id"`
	ResourceID             string                    `json:"resource_id"`
	ProviderID             string                    `json:"provider_id"`
	ServiceType            attachmentsv1.ServiceType `json:"service_type"`
	PlanID                 string                    `json:"plan_id"`
	PlanVersion            int64                     `json:"plan_version"`
	Provider               string                    `json:"provider"`
	ProviderPlan           string                    `json:"provider_plan"`
	ProviderMappingVersion string                    `json:"provider_mapping_version"`
	BackupBeforePurge      bool                      `json:"backup_before_purge"`
}

func NewProviderResourceDestroyPlan(instance ServiceInstance, plan ServicePlan) (ProviderResourceDestroyPlan, error) {
	value := ProviderResourceDestroyPlan{
		Action:                 "destroy",
		TenantID:               strings.TrimSpace(instance.TenantID),
		ResourceID:             strings.TrimSpace(instance.ID),
		ProviderID:             strings.TrimSpace(instance.ProviderID),
		ServiceType:            instance.Type,
		PlanID:                 strings.TrimSpace(plan.ID),
		PlanVersion:            plan.Version,
		Provider:               strings.TrimSpace(plan.Provider),
		ProviderPlan:           strings.TrimSpace(plan.ProviderPlan),
		ProviderMappingVersion: strings.TrimSpace(plan.ProviderMappingVersion),
		BackupBeforePurge:      plan.BackupBeforePurge,
	}
	if value.TenantID == "" || value.ResourceID == "" || value.ProviderID == "" || value.PlanID == "" || value.PlanVersion < 1 ||
		value.Provider == "" || value.ProviderPlan == "" || value.ProviderMappingVersion == "" || instance.PlanID != plan.ID || instance.PlanVersion != plan.Version || instance.Type != plan.Type {
		return ProviderResourceDestroyPlan{}, NewError(CodeInvalidArgument, "invalid provider resource destroy plan")
	}
	return value, nil
}

func (p ProviderResourceDestroyPlan) PlanHash() string {
	return "sha256:" + Hash(p)
}

func NewServicePlan(id string, version int64, serviceType attachmentsv1.ServiceType, provider, providerPlan, mappingVersion string, dedicated bool, capabilities []string, enabled, backupBeforePurge bool, createdAt time.Time) (ServicePlan, error) {
	plan := ServicePlan{ID: strings.TrimSpace(id), Version: version, Type: serviceType, Provider: strings.TrimSpace(provider), ProviderPlan: strings.TrimSpace(providerPlan), ProviderMappingVersion: strings.TrimSpace(mappingVersion), Dedicated: dedicated, Capabilities: CanonicalStrings(capabilities), Enabled: enabled, BackupBeforePurge: backupBeforePurge, CreatedAt: createdAt.UTC()}
	if plan.ID == "" || plan.Version < 1 || plan.Provider == "" || plan.ProviderPlan == "" || plan.ProviderMappingVersion == "" || plan.CreatedAt.IsZero() || len(plan.Capabilities) == 0 {
		return ServicePlan{}, NewError(CodeInvalidArgument, "invalid service plan")
	}
	switch serviceType {
	case attachmentsv1.ServicePostgreSQL, attachmentsv1.ServiceRedis, attachmentsv1.ServiceS3:
	default:
		return ServicePlan{}, NewError(CodeInvalidArgument, "invalid service type")
	}
	return plan, nil
}

type ServiceInstance struct {
	ID                   string                     `json:"id"`
	TenantID             string                     `json:"tenant_id"`
	ApplicationID        string                     `json:"application_id,omitempty"`
	EnvironmentID        string                     `json:"environment_id,omitempty"`
	Name                 string                     `json:"name"`
	PlanID               string                     `json:"plan_id"`
	PlanVersion          int64                      `json:"plan_version"`
	Type                 attachmentsv1.ServiceType  `json:"type"`
	State                attachmentsv1.ServiceState `json:"state"`
	ProviderOperationKey string                     `json:"provider_operation_key"`
	ProviderID           string                     `json:"provider_id,omitempty"`
	ProviderEndpoint     string                     `json:"provider_endpoint,omitempty"`
	FailureCode          string                     `json:"failure_code,omitempty"`
	FailureMessage       string                     `json:"failure_message,omitempty"`
	FinalBackupID        string                     `json:"final_backup_id,omitempty"`
	Version              int64                      `json:"version"`
	CreatedAt            time.Time                  `json:"created_at"`
	UpdatedAt            time.Time                  `json:"updated_at"`
}

type ServiceBinding struct {
	ID                    string                     `json:"id"`
	TenantID              string                     `json:"tenant_id"`
	ApplicationID         string                     `json:"application_id"`
	EnvironmentID         string                     `json:"environment_id"`
	InstanceID            string                     `json:"instance_id"`
	State                 attachmentsv1.BindingState `json:"state"`
	Capabilities          []string                   `json:"capabilities"`
	ProviderCredentialID  string                     `json:"provider_credential_id,omitempty"`
	CredentialRef         string                     `json:"credential_ref,omitempty"`
	PreviousCredentialID  string                     `json:"previous_credential_id,omitempty"`
	PreviousCredentialRef string                     `json:"previous_credential_ref,omitempty"`
	SnapshotID            string                     `json:"snapshot_id,omitempty"`
	Version               int64                      `json:"version"`
	CreatedAt             time.Time                  `json:"created_at"`
	UpdatedAt             time.Time                  `json:"updated_at"`
}

type DomainClaim struct {
	ID                   string                    `json:"id"`
	TenantID             string                    `json:"tenant_id"`
	ApplicationID        string                    `json:"application_id"`
	EnvironmentID        string                    `json:"environment_id"`
	Hostname             string                    `json:"hostname"`
	Generated            bool                      `json:"generated"`
	State                attachmentsv1.DomainState `json:"state"`
	ChallengeName        string                    `json:"challenge_name,omitempty"`
	ChallengeValue       string                    `json:"challenge_value,omitempty"`
	FirstObservationHash string                    `json:"first_observation_hash,omitempty"`
	FirstObservedAt      time.Time                 `json:"first_observed_at,omitempty"`
	CertificateID        string                    `json:"certificate_id,omitempty"`
	FailureCode          string                    `json:"failure_code,omitempty"`
	QuarantineUntil      time.Time                 `json:"quarantine_until,omitempty"`
	Version              int64                     `json:"version"`
	CreatedAt            time.Time                 `json:"created_at"`
	UpdatedAt            time.Time                 `json:"updated_at"`
}

type AttachmentSnapshot struct {
	Value       attachmentsv1.AttachmentSnapshot `json:"value"`
	ContentHash string                           `json:"content_hash"`
}

type IdempotencyRecord struct {
	TenantID    string    `json:"tenant_id"`
	Scope       string    `json:"scope"`
	Key         string    `json:"key"`
	RequestHash string    `json:"request_hash"`
	ResourceID  string    `json:"resource_id"`
	CreatedAt   time.Time `json:"created_at"`
}

type OutboxRecord struct {
	ID          string          `json:"id"`
	TenantID    string          `json:"tenant_id"`
	Topic       string          `json:"topic"`
	AggregateID string          `json:"aggregate_id"`
	Payload     json.RawMessage `json:"payload"`
	CreatedAt   time.Time       `json:"created_at"`
}

type AuditRecord struct {
	ID           string          `json:"id"`
	TenantID     string          `json:"tenant_id"`
	ActorID      string          `json:"actor_id"`
	Action       string          `json:"action"`
	ResourceType string          `json:"resource_type"`
	ResourceID   string          `json:"resource_id"`
	Data         json.RawMessage `json:"data"`
	CreatedAt    time.Time       `json:"created_at"`
}
