# Итерация 1. Platform Kernel

## Результат итерации

Появляется минимальный надёжный control-plane kernel, на который смогут опереться все последующие домены:

- organizations;
- memberships;
- principals;
- authorization;
- idempotent commands;
- asynchronous operations;
- transactional outbox/inbox;
- append-only audit.

В этой итерации нет GitLab, builds, deployments, Kubernetes, services и billing.

## Владение данными

PostgreSQL schema: `kernel`.

Агрегаты:

- `Organization`;
- `Membership`;
- `Operation`;
- `IdempotencyRecord`;
- `AuditRecord`.

## Invariants

1. Создатель организации становится `owner` атомарно.
2. Principal без активного membership не может обращаться к tenant resource.
3. Cross-tenant access всегда deny по умолчанию.
4. У одной organization должен оставаться хотя бы один owner.
5. Операция не может перейти назад по state machine.
6. Один idempotency key с одним payload создаёт одну side effect.
7. Domain change и outbox event коммитятся в одной транзакции.
8. Audit append-only; update/delete запрещены.
9. В audit не попадают secret values и provider tokens.

## State machines

### Membership

```text
INVITED → ACTIVE → SUSPENDED → REMOVED
             ↘ REMOVED
```

### Operation

```text
PENDING → RUNNING → WAITING_EXTERNAL → RUNNING → SUCCEEDED
                    ↘ FAILED
PENDING/RUNNING/WAITING_EXTERNAL → CANCELED
```

Терминальные состояния не изменяются.

## Публичные команды

```text
CreateOrganization
InviteMember
AcceptInvitation
ChangeMemberRole
SuspendMembership
RemoveMembership
StartOperation
CancelOperation
GetOperation
ListAuditEvents
```

## События

```text
kernel.organization_created.v1
kernel.membership_changed.v1
kernel.operation_state_changed.v1
kernel.audit_recorded.v1
```

## TDD-последовательность

Писать тесты именно в указанном порядке.

### 1. Organization aggregate

```text
TestOrganization_Create_AssignsCreatorAsOwner
TestOrganization_Create_RejectsEmptyName
TestOrganization_Create_NormalizesSlug
TestOrganization_CannotRemoveLastOwner
```

### 2. Membership authorization

```text
TestMembership_InvitationDoesNotGrantAccess
TestMembership_AcceptanceGrantsConfiguredRole
TestAuthorization_DeniesCrossTenantAccess
TestAuthorization_DeniesMissingScope
TestAuthorization_AllowsOwnerScope
TestMembership_SuspendedPrincipalIsDenied
```

### 3. Operation state machine

```text
TestOperation_StartsPending
TestOperation_AllowedTransitionMatrix
TestOperation_RejectsBackwardTransition
TestOperation_TerminalStateIsImmutable
TestOperation_CancelIsIdempotent
TestOperation_RecordsStableErrorCode
```

Использовать property-based test для всех комбинаций state transitions.

### 4. Idempotency

```text
TestIdempotency_SameKeyAndPayloadReturnsOriginalResult
TestIdempotency_SameKeyDifferentPayloadReturnsConflict
TestIdempotency_ConcurrentSameKeyCreatesOneOrganization
TestIdempotency_RetryAfterTransportFailureReturnsExistingOperation
```

### 5. Transactional outbox/inbox

```text
TestOutbox_AggregateAndEventCommitAtomically
TestOutbox_RollbackLeavesNeitherAggregateNorEvent
TestOutbox_DispatchMarksEventAfterSuccessfulPublish
TestOutbox_PublishFailureLeavesEventPending
TestInbox_DuplicateEventIsIgnored
TestInbox_SameEventIDWithDifferentPayloadIsRejected
```

### 6. Audit

```text
TestAudit_EveryMutationProducesAuditRecord
TestAudit_RecordContainsActorTenantCorrelationAndOutcome
TestAudit_IsAppendOnlyAtRepositoryBoundary
TestAudit_RedactsSensitiveFields
TestAudit_FailedAuthorizationIsRecorded
```

### 7. Identity-provider adapter contract

```text
TestOIDCClaims_MapsSubjectToPrincipal
TestOIDCClaims_RejectsMissingSubject
TestOIDCClaims_RejectsWrongIssuer
TestOIDCClaims_DoesNotTrustTenantFromUnsignedInput
```

## Integration tests

С реальным PostgreSQL:

```text
TestPostgres_CreateOrganizationAndOutboxAreAtomic
TestPostgres_ConcurrentIdempotencyUsesSingleWinner
TestPostgres_OperationOptimisticLockPreventsLostUpdate
TestPostgres_AuditTableRejectsUpdateAndDelete
TestPostgres_Migrations_CleanInstallAndUpgrade
```

## Acceptance-сценарий

```gherkin
Feature: Tenant kernel

  Scenario: Owner creates an organization and invites a developer
    Given an authenticated user principal
    When the user creates organization "Acme"
    Then the operation eventually succeeds
    And the user is the owner of organization "Acme"
    When the owner invites another user as developer
    Then the invitation does not grant access before acceptance
    When the invited user accepts
    Then the developer can read organization resources
    And the developer cannot manage owners
    And all actions are visible in the audit log
```

## Порты, которые замораживаются

```go
type Authorizer interface {
    Check(ctx context.Context, principal PrincipalContext, action string, resource ResourceRef) error
}

type OperationService interface {
    Start(ctx context.Context, cmd CommandMeta) (OperationRef, error)
    Transition(ctx context.Context, id OperationID, to OperationState, result OperationResult) error
    Get(ctx context.Context, id OperationID) (OperationSnapshot, error)
}

type EventPublisher interface {
    Publish(ctx context.Context, event EventEnvelope[json.RawMessage]) error
}
```

## Не входит в итерацию

- GitLab user/group provisioning;
- project/repository;
- plan limits;
- billing;
- AI approval policy;
- runtime quota.

До итерации 6 entitlement port использует allow-all development adapter.

## Exit gate

Итерация закрыта, когда:

- concurrent idempotency test стабильно green;
- outbox atomicity доказана real-Postgres test;
- cross-tenant negative suite green;
- audit redaction suite green;
- API acceptance scenario green;
- `PrincipalContext`, `OperationRef` и event envelope опубликованы как v1.
