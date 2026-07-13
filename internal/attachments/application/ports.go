package application

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/keir-research/ai-native-paas/internal/attachments/domain"
	attachmentsv1 "github.com/keir-research/ai-native-paas/pkg/contracts/attachments/v1"
	commercev1 "github.com/keir-research/ai-native-paas/pkg/contracts/commerce/v1"
)

type Store interface {
	Transact(context.Context, func(Tx) error) error
}

type Tx interface {
	GetSecretSet(string) (domain.SecretSet, bool)
	FindSecretSet(string, string) (domain.SecretSet, bool)
	PutSecretSet(domain.SecretSet, int64) error
	GetSecret(string) (domain.SecretMetadata, bool)
	FindSecret(string, string, attachmentsv1.SecretScope) (domain.SecretMetadata, bool)
	ListSecrets(string) []domain.SecretMetadata
	PutSecret(domain.SecretMetadata, int64) error
	GetPlan(string, int64) (domain.ServicePlan, bool)
	LatestPlan(string) (domain.ServicePlan, bool)
	ListPlans() []domain.ServicePlan
	PutPlan(domain.ServicePlan) error
	GetInstance(string) (domain.ServiceInstance, bool)
	FindInstance(string, string, string) (domain.ServiceInstance, bool)
	ListInstances(string, string) []domain.ServiceInstance
	PutInstance(domain.ServiceInstance, int64) error
	GetBinding(string) (domain.ServiceBinding, bool)
	FindBinding(string, string, string) (domain.ServiceBinding, bool)
	ListBindings(string, string) []domain.ServiceBinding
	ListAppBindings(string, string) []domain.ServiceBinding
	PutBinding(domain.ServiceBinding, int64) error
	GetClaim(string) (domain.DomainClaim, bool)
	FindClaim(string) (domain.DomainClaim, bool)
	ListClaims(string, string) []domain.DomainClaim
	PutClaim(domain.DomainClaim, int64) error
	GetSnapshot(string) (domain.AttachmentSnapshot, bool)
	LatestSnapshot(string, string) (domain.AttachmentSnapshot, bool)
	FindSnapshot(string, string, string) (domain.AttachmentSnapshot, bool)
	PutSnapshot(domain.AttachmentSnapshot) error
	GetIdempotency(string, string, string) (domain.IdempotencyRecord, bool)
	PutIdempotency(domain.IdempotencyRecord) error
	AppendOutbox(domain.OutboxRecord) error
	AppendAudit(domain.AuditRecord) error
}

type EnvironmentRef struct {
	TenantID, ApplicationID, EnvironmentID, Name string
	Ready                                        bool
}

type EnvironmentDirectory interface {
	ResolveEnvironment(context.Context, string, string) (EnvironmentRef, error)
}

type SecretWriteResult struct {
	Ref, Version string
}

type SecretProviderMetadata struct {
	Ref, Version string
	ExpiresAt    time.Time
	Exists       bool
}

type SecretProvider interface {
	Write(context.Context, string, string, string, []byte, time.Time) (SecretWriteResult, error)
	Metadata(context.Context, string, string, string) (SecretProviderMetadata, error)
	Delete(context.Context, string, string, string) error
}

type ProviderInstanceResult struct {
	ProviderID, Endpoint, FailureCode string
	Ready, FailedFinal                bool
}

type ProviderCredential struct {
	CredentialID string
	Values       map[string][]byte
	Capabilities []string
}

type ManagedServiceProvider interface {
	EnsureInstance(context.Context, domain.ServicePlan, domain.ServiceInstance, map[string]string) (ProviderInstanceResult, error)
	FindInstance(context.Context, domain.ServicePlan, domain.ServiceInstance) (ProviderInstanceResult, bool, error)
	GetInstance(context.Context, domain.ServicePlan, domain.ServiceInstance) (ProviderInstanceResult, error)
	IssueCredential(context.Context, domain.ServicePlan, domain.ServiceInstance, domain.ServiceBinding) (ProviderCredential, error)
	RevokeCredential(context.Context, domain.ServicePlan, domain.ServiceInstance, string) error
	CreateBackup(context.Context, domain.ServicePlan, domain.ServiceInstance) (string, error)
	DeleteInstance(context.Context, domain.ServicePlan, domain.ServiceInstance) error
}

type DNSResolver interface {
	ReadTXT(context.Context, string) ([]string, error)
}

type CertificateStatus string

const (
	CertificatePending CertificateStatus = "PENDING"
	CertificateReady   CertificateStatus = "READY"
	CertificateFailed  CertificateStatus = "FAILED"
)

type CertificateResult struct {
	CertificateID, FailureCode string
	Status                     CertificateStatus
}

type CertificateProvider interface {
	Ensure(context.Context, string, string) (CertificateResult, error)
	Status(context.Context, string) (CertificateResult, error)
}

type ApprovalVerifier interface {
	Verify(context.Context, string, string, string, string) error
}

type RuntimeSnapshotPublisher interface {
	Publish(context.Context, attachmentsv1.AttachmentSnapshot) error
}

type CommercialEntitlementPort interface {
	Check(context.Context, commercev1.EntitlementRequest) (commercev1.EntitlementDecision, error)
}

type UsageSink interface {
	Append(context.Context, commercev1.UsageEvent) error
}

type StructuredLogger interface {
	Log(context.Context, string, map[string]string)
}

type Clock interface{ Now() time.Time }
type IDGenerator interface{ New(string) string }

type RealClock struct{}

func (RealClock) Now() time.Time { return time.Now().UTC() }

type SequentialIDs struct{ value atomic.Uint64 }

func (g *SequentialIDs) New(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, g.value.Add(1))
}

type ProviderError struct {
	Code      string
	Retryable bool
	Cause     error
}

func (e *ProviderError) Error() string {
	if e.Cause != nil {
		return e.Code + ": " + e.Cause.Error()
	}
	return e.Code
}

func (e *ProviderError) Unwrap() error { return e.Cause }

type SetSecretRequest struct {
	TenantID, ApplicationID, EnvironmentID, Name string
	Scope                                        attachmentsv1.SecretScope
	Phase                                        attachmentsv1.SecretPhase
	Value                                        []byte
	ExpiresAt                                    time.Time
	ActorID, IdempotencyKey                      string
}

type DeleteSecretRequest struct{ TenantID, EnvironmentID, SecretID, ActorID, IdempotencyKey string }
type ProvisionServiceRequest struct {
	TenantID, ApplicationID, EnvironmentID, Name, PlanID string
	PlanVersion                                          int64
	ActorID, IdempotencyKey                              string
}
type BindServiceRequest struct {
	TenantID, ApplicationID, EnvironmentID, InstanceID string
	Capabilities                                       []string
	ActorID, IdempotencyKey                            string
}
type RotateBindingRequest struct{ TenantID, BindingID, ActorID, IdempotencyKey string }
type RevokeBindingRequest struct{ TenantID, BindingID, ActorID, IdempotencyKey string }
type PurgeServiceRequest struct{ TenantID, InstanceID, ApprovalRef, ActorID, IdempotencyKey string }
type GeneratedDomainRequest struct{ TenantID, ApplicationID, EnvironmentID, PreferredName, ActorID, IdempotencyKey string }
type ClaimDomainRequest struct{ TenantID, ApplicationID, EnvironmentID, Hostname, ActorID, IdempotencyKey string }
type VerifyDomainRequest struct{ TenantID, ClaimID, ActorID string }
type DeleteDomainRequest struct{ TenantID, ClaimID, ActorID, IdempotencyKey string }
