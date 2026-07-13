# Итерация 6. Commercial Governance

## Результат итерации

Платформа принимает коммерчески корректные решения до выполнения операций и формирует детерминированный usage ledger:

- versioned plans;
- entitlements;
- quota reservations;
- runtime/build/storage/network usage;
- rating;
- invoice preview;
- grace period;
- suspension/resumption.

Платёжный провайдер можно подключить позже. Источник истины — внутренний ledger.

## Зависимости

Только опубликованные usage/operation references предыдущих доменов:

- tenant/application/environment/build/deployment/service identifiers;
- operation outcomes;
- observed runtime allocation;
- provider usage counters.

Commerce не читает внутренние таблицы других schemas.

## Владение данными

PostgreSQL schema: `commerce`.

Агрегаты:

- `PlanDefinition`;
- `PlanVersion`;
- `Subscription`;
- `EntitlementSet`;
- `QuotaReservation`;
- `UsageEvent`;
- `RatingRule`;
- `RatedUsage`;
- `BillingPeriod`;
- `CommercialAccountState`.

## Invariants

1. Plan version после активации immutable.
2. Price change действует только prospectively.
3. Usage event append-only и idempotent.
4. Duplicate telemetry не увеличивает charge.
5. Runtime units считаются по allocation time.
6. Platform failure не тарифицируется по policy.
7. User-code build failure может тарифицироваться.
8. Quota reservation атомарна при concurrency.
9. Suspension останавливает compute, но не уничтожает data.
10. Resumption сохраняет application/service identities.
11. Все расчёты используют UTC и integer minor units.
12. Invoice preview воспроизводим из immutable inputs.

## State machines

### Subscription

```text
TRIAL → ACTIVE → GRACE → SUSPENDED → ACTIVE
ACTIVE/GRACE/SUSPENDED → CANCELED
```

### Quota reservation

```text
REQUESTED → RESERVED → COMMITTED
                     ↘ RELEASED
REQUESTED → REJECTED
```

### Billing period

```text
OPEN → CLOSING → CLOSED → INVOICED
```

После `CLOSED` исходные usage records не изменяются; correction оформляется отдельной записью.

## Meter catalog v1

```text
runtime.unit_seconds
runtime.extra_replica_seconds
build.cpu_seconds
build.memory_gib_seconds
build.docker_vm_seconds
artifact.storage_gib_hours
persistent.storage_gib_hours
database.plan_seconds
object_storage.gib_hours
egress.bytes
logs.ingested_bytes
logs.retained_gib_hours
```

## TDD-последовательность

### 1. Plan versioning

```text
TestPlan_ActiveVersionIsImmutable
TestPlan_NewVersionDoesNotChangeExistingPeriod
TestPlan_DisabledVersionRejectsNewSubscription
TestPlan_MeterReferencesMustExist
TestPlan_PricesUseIntegerMinorUnits
```

### 2. Entitlement decisions

```text
TestEntitlement_AllowsFeatureIncludedInPlan
TestEntitlement_DeniesMissingFeature
TestEntitlement_DeniesSuspendedAccount
TestEntitlement_TrialHasExplicitLimits
TestEntitlement_DecisionContainsReasonAndPolicyVersion
TestEntitlement_CrossTenantLookupDenied
```

### 3. Quota reservation

```text
TestQuota_ReserveWithinLimit
TestQuota_RejectAboveLimit
TestQuota_ConcurrentReservationsCannotOversubscribe
TestQuota_ReleaseMakesCapacityAvailable
TestQuota_CommitIsIdempotent
TestQuota_ExpiredReservationIsReclaimed
```

### 4. Usage ingestion

```text
TestUsage_AppendValidEvent
TestUsage_DuplicateIdempotencyKeyIgnored
TestUsage_SameKeyDifferentQuantityRejected
TestUsage_NegativeQuantityRejectedExceptCorrectionType
TestUsage_EventOutsideTenantResourceRejected
TestUsage_LateEventRoutesToCorrectOpenPeriod
```

### 5. Runtime unit calculation

```text
TestRuntimeUsage_OneUnitForOneHourEquals3600UnitSeconds
TestRuntimeUsage_ReplicaScaleProducesPiecewiseUsage
TestRuntimeUsage_SuspensionStopsComputeUsageAtBoundary
TestRuntimeUsage_ClockSkewDoesNotCreateNegativeInterval
TestRuntimeUsage_OverlappingObservationsAreDeduplicated
```

Использовать property-based tests для произвольной последовательности scale events.

### 6. Build rating policy

```text
TestBuildRating_SuccessIsCharged
TestBuildRating_UserFailureIsChargedByPolicy
TestBuildRating_PlatformFailureIsNotCharged
TestBuildRating_CanceledBeforeStartIsNotCharged
TestBuildRating_DockerVMUsesSeparateMeter
```

### 7. Time and rounding

```text
TestRating_UsesUTCPeriodBoundaries
TestRating_DaylightSavingHasNoEffect
TestRating_RoundsOnlyAtDefinedStage
TestRating_MicroUsageAccumulatesBeforeRounding
TestRating_LargeQuantityDoesNotOverflow
```

### 8. Invoice preview

```text
TestInvoicePreview_IsDeterministic
TestInvoicePreview_GroupsByMeterAndResource
TestInvoicePreview_AppliesIncludedAllowanceBeforeOverage
TestInvoicePreview_CreditsAreSeparateLedgerEntries
TestInvoicePreview_PriceVersionMatchesUsageTimestamp
```

### 9. Suspension/resumption

```text
TestCommercialState_GracePeriodDoesNotDeleteRuntime
TestCommercialState_SuspensionEmitsSuspendIntent
TestCommercialState_SuspensionRetainsManagedServices
TestCommercialState_ResumptionEmitsResumeIntent
TestCommercialState_AlreadySuspendedIsIdempotent
```

### 10. Reconciliation

```text
TestUsageReconciler_ObservedAllocationMatchesLedger
TestUsageReconciler_MissingIntervalCreatesCorrection
TestUsageReconciler_DuplicateCorrectionIsPrevented
TestUsageReconciler_DriftAboveThresholdRaisesAlert
```

## Contract with other domains

Consumers call:

```go
type EntitlementService interface {
    Check(ctx context.Context, req EntitlementRequest) (EntitlementDecision, error)
    Reserve(ctx context.Context, req QuotaRequest) (QuotaReservation, error)
    Commit(ctx context.Context, reservationID string) error
    Release(ctx context.Context, reservationID string) error
}
```

Producers emit usage through:

```go
type UsageSink interface {
    Append(ctx context.Context, event UsageEvent) error
}
```

## Acceptance-сценарий

```gherkin
Feature: Commercially governed deployment

  Scenario: A tenant consumes runtime units and reaches a quota
    Given an active subscription with two runtime units
    When the tenant deploys two one-unit processes
    Then both quota reservations succeed
    When the tenant requests a third one-unit process
    Then the request is rejected before Kubernetes mutation
    And the reason references the active plan version
    When one process runs for one hour
    Then exactly 3600 runtime.unit_seconds are recorded
    And replaying the same usage event does not change the invoice preview
```

## Не входит в итерацию

- card payments;
- tax calculation;
- dunning emails;
- accounting export;
- agent approval UX;
- sales discounts outside explicit ledger credits.

## Exit gate

- property-based runtime usage suite green;
- concurrent quota test green;
- deterministic invoice golden green;
- duplicate usage cannot double charge;
- suspension retains data in acceptance test;
- `EntitlementDecision` и `UsageEvent` frozen as v1.
