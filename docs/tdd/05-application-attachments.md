# Итерация 5. Application Attachments

## Результат итерации

Приложение получает безопасные внешние зависимости через ограниченную attachment model:

- runtime/build secrets;
- managed service instances;
- service bindings;
- generated/custom domains;
- TLS lifecycle.

Runtime domain потребляет только immutable `AttachmentSnapshot`; он не знает, как provision-ится PostgreSQL или хранится secret.

## Зависимости

- Kernel contracts;
- Application/Environment references из runtime;
- runtime port для применения нового attachment snapshot.

## Владение данными

PostgreSQL schema: `attachments`.

Агрегаты:

- `SecretSet`;
- `SecretMetadata`;
- `ServiceCatalogEntry`;
- `ServiceInstance`;
- `ServiceBinding`;
- `DomainClaim`;
- `AttachmentSnapshot`.

Secret values находятся только в OpenBao.

## Invariants

1. Secret value после записи не возвращается через API.
2. Runtime и build secrets разделены.
3. Secret value не появляется в GitOps, logs, events и audit.
4. Service provisioning idempotent.
5. Удаление app не удаляет service instance.
6. Binding revoke прекращает доступ и инициирует rotation.
7. Destructive service purge требует approval reference.
8. Custom domain route создаётся только после ownership verification.
9. Domain name принадлежит не более чем одному active claim.
10. Удалённый domain проходит quarantine перед повторным назначением.
11. Attachment snapshot immutable и versioned.

## State machines

### Service instance

```text
REQUESTED → PROVISIONING → READY
                    ↘ FAILED_RETRYABLE
                    ↘ FAILED_FINAL
READY → SUSPENDING → SUSPENDED
READY/SUSPENDED → DELETING → RETAINED_BACKUP → DELETED
```

### Binding

```text
REQUESTED → ISSUING_CREDENTIALS → ACTIVE → ROTATING → ACTIVE
                                      ↘ REVOKING → REVOKED
```

### Domain claim

```text
REQUESTED → AWAITING_VERIFICATION → VERIFIED → TLS_PENDING → ACTIVE
                                         ↘ VERIFICATION_FAILED
ACTIVE → DETACHING → QUARANTINED → RELEASED
```

## TDD-последовательность

### 1. Secret metadata/value separation

```text
TestSecret_SetStoresValueOnlyThroughVaultPort
TestSecret_GetReturnsMetadataNotValue
TestSecret_UpdateCreatesNewVersion
TestSecret_DeleteMarksMetadataDeletedAndRemovesVaultValue
TestSecret_NameValidationRejectsReservedVariables
TestSecret_CrossTenantAccessDenied
```

### 2. Secret leakage suite

Использовать sentinel secret `TDD_SECRET_DO_NOT_LEAK_...`.

```text
TestSecret_NotPresentInAudit
TestSecret_NotPresentInDomainEvents
TestSecret_NotPresentInPublicErrors
TestSecret_NotPresentInGitOpsManifest
TestSecret_NotPresentInBuildMetadata
TestSecret_NotPresentInStructuredLogs
```

### 3. Build/runtime separation

```text
TestSecret_RuntimeSecretCannotBeRequestedByBuild
TestSecret_BuildSecretCannotBeMountedAtRuntimeUnlessExplicitlyDuplicated
TestSecret_BuildSecretHasPhaseAndTTL
TestSecret_RuntimeRotationTriggersControlledRestart
```

### 4. Service catalog

```text
TestServiceCatalog_RejectsUnknownPlan
TestServiceCatalog_PlanHasImmutableProviderMappingVersion
TestServiceCatalog_SharedAndDedicatedPlansExposeDifferentCapabilities
TestServiceCatalog_DisabledPlanCannotCreateNewInstance
```

### 5. Provisioning saga

```text
TestService_ProvisionIsIdempotent
TestService_ProviderTimeoutLeavesRetryableState
TestService_ReconcileFindsProviderResourceAfterLostResponse
TestService_ReadyOnlyAfterProviderConditionReady
TestService_FailedFinalHasStableUserError
TestService_ProviderResourceTaggedWithTenantAndInstanceIDs
```

### 6. Bindings

```text
TestBinding_RequiresReadyServiceAndValidEnvironment
TestBinding_CreatesLeastPrivilegeCredential
TestBinding_StoresCredentialInOpenBao
TestBinding_AttachmentSnapshotContainsReferenceNotValue
TestBinding_RotationReplacesCredentialAtomically
TestBinding_RevokeRemovesRuntimeAccess
TestBinding_DeleteApplicationRevokesBindingButRetainsService
```

### 7. Destructive deletion

```text
TestServicePurge_RequiresValidApprovalReference
TestServicePurge_ApprovalMustMatchInstanceAndActor
TestServicePurge_CreatesFinalBackupWhenPolicyRequires
TestServicePurge_IsIrreversibleAfterProviderDelete
TestServicePurge_AuditContainsNoCredential
```

### 8. Generated domain

```text
TestGeneratedDomain_IsDeterministicAndTenantUnique
TestGeneratedDomain_ReservedNamesRejected
TestGeneratedDomain_RoutePointsToCorrectEnvironment
```

### 9. Custom domain verification

```text
TestDomainClaim_DuplicateActiveClaimRejected
TestDomainClaim_ReturnsTXTChallenge
TestDomainClaim_WrongTXTDoesNotVerify
TestDomainClaim_CorrectTXTVerifiesOwnership
TestDomainClaim_DNSRebindingDuringVerificationIsRejected
TestDomainClaim_RouteNotCreatedBeforeVerification
TestDomainClaim_TLSPendingDoesNotReportActive
TestDomainClaim_CertificateReadyActivatesClaim
TestDomainClaim_DeleteEntersQuarantine
TestDomainClaim_QuarantinePreventsImmediateTakeover
```

### 10. Attachment snapshot

```text
TestAttachmentSnapshot_IsImmutable
TestAttachmentSnapshot_ChangesVersionWhenBindingChanges
TestAttachmentSnapshot_SecretRotationChangesVersionWithoutValueDisclosure
TestAttachmentSnapshot_RuntimeUpdateIsIdempotent
TestAttachmentSnapshot_DoesNotIncludeDeletedBinding
```

## Adapter contracts

### OpenBao

```text
TestOpenBaoAdapter_WriteReadMetadataDelete
TestOpenBaoAdapter_UsesTenantScopedPath
TestOpenBaoAdapter_DeniesCrossTenantPath
TestOpenBaoAdapter_MapsUnavailableToRetryableError
```

### Cozystack

```text
TestCozystackAdapter_CreatePostgresWithPlanMapping
TestCozystackAdapter_CreateRedisWithTenantLabels
TestCozystackAdapter_ReadReadyCondition
TestCozystackAdapter_FindExistingResourceByInstanceID
TestCozystackAdapter_DeleteRequiresExplicitCall
```

### DNS/TLS

```text
TestDNSAdapter_ReadTXTChallenge
TestDNSAdapter_HandlesMultipleTXTValues
TestCertificateAdapter_MapsPendingReadyFailed
```

## Acceptance-сценарий

```gherkin
Feature: Safe application attachments

  Scenario: Bind PostgreSQL and a custom domain to production
    Given a ready production environment
    When a PostgreSQL service is provisioned
    Then the service eventually becomes Ready
    When the service is bound to the environment
    Then credentials are stored in OpenBao
    And runtime receives only a secret reference
    And no credential appears in GitOps or logs
    When the user proves ownership of a custom domain
    Then the route and TLS certificate become active
    When the application is deleted
    Then the binding is revoked
    And the PostgreSQL service is retained
```

## Порты, которые замораживаются

```go
type AttachmentSnapshot struct {
    SnapshotID       string
    SecretSetRef     string
    ServiceBindings  []string
    ActiveDomains    []string
    Version          int64
}

type AttachmentResolver interface {
    Resolve(ctx context.Context, environmentID string) (AttachmentSnapshot, error)
}
```

## Не входит в итерацию

- price/rating managed services;
- invoice generation;
- AI approval issuance;
- production data masking for preview clones;
- cross-region database replication.

Approval port для purge использует test/admin adapter до итерации 7.

## Exit gate

- sentinel secret leakage suite fully green;
- Postgres provision/bind/revoke acceptance green;
- app deletion retains service;
- custom domain cannot route before verification;
- attachment snapshot contract frozen as v1.
