# Итерация 4. Runtime Delivery

## Результат итерации

Платформа разворачивает готовый `ArtifactRef` в Kubernetes через GitOps и Argo CD, используя собственный `PaaSApp` CRD/operator.

В конце итерации работает путь:

```text
ArtifactRef → Release → GitOps commit → Argo CD → PaaSApp → Deployment/Service/Route → Ready URL
```

Managed services, custom domains и billing ещё не реализованы.

## Зависимости

- Kernel contracts;
- `ArtifactRef` и `ArtifactPolicy`;
- audit/operations/idempotency.

## Владение данными

PostgreSQL schema: `runtime`.

Агрегаты:

- `Application`;
- `Environment`;
- `Release`;
- `Deployment`;
- `RuntimeCell`;
- `Placement`;
- `GitOpsCommitRecord`.

Kubernetes является observed-state source, GitOps repository — desired-state source.

## Invariants

1. Release создаётся только из `RELEASABLE` artifact.
2. Environment имеет не более одного active release.
3. Runtime использует exact image digest.
4. Placement стабилен до explicit migration.
5. Один Kubernetes resource имеет одного authoritative controller.
6. Argo управляет `PaaSApp`; operator управляет child resources.
7. Success означает `PaaSApp Ready`, а не только Argo `Synced`.
8. Rollback переиспользует старый digest и не rebuild-ит код.
9. Namespace выделяется на app + environment.
10. Shared-tier pod всегда получает sandbox/security defaults.
11. Удаление двухфазное и не уничтожает external data.

## State machines

### Release

```text
CREATED → VALIDATED → COMMITTED_TO_GITOPS → DEPLOYING → ACTIVE
                                      ↘ FAILED
ACTIVE → SUPERSEDED
```

### Deployment

```text
PENDING → GIT_COMMITTED → ARGO_SYNCING → ROLLING_OUT → READY
                                     ↘ DEGRADED
                                     ↘ FAILED
```

### Application lifecycle

```text
ACTIVE → SUSPENDING → SUSPENDED → RESUMING → ACTIVE
ACTIVE/SUSPENDED → DELETING_RUNTIME → RETAINING → DELETED
```

## `PaaSApp` contract

Минимальная v1alpha1 spec:

```yaml
apiVersion: platform.example.com/v1alpha1
kind: PaaSApp
metadata:
  name: app-456-production
spec:
  tenantID: tnt-123
  appID: app-456
  environment: production
  releaseID: rel-0192
  image:
    repository: harbor.example.com/tenants/tnt-123/apps/app-456/image
    digest: sha256:...
  runtime:
    isolation: sandboxed
    unit: u1
  processes:
    web:
      port: 8080
      minReplicas: 1
      maxReplicas: 3
      healthPath: /health
  route:
    generatedHostname: booking--acme.apps.eu1.example.com
  attachmentSnapshotRef: none
  lifecycle:
    state: Active
```

## TDD-последовательность

### 1. Application/environment model

```text
TestApplication_CreateBelongsToTenantProject
TestEnvironment_DefaultProductionIsUnique
TestEnvironment_NameIsUniqueWithinApplication
TestEnvironment_NamespaceNameIsDeterministic
TestEnvironment_CannotChangeTenant
```

### 2. Release validation

```text
TestRelease_RejectsNonReleasableArtifact
TestRelease_StoresExactDigest
TestRelease_SameArtifactAndConfigIsIdempotent
TestRelease_ConfigChangeCreatesNewRelease
TestRelease_OnlyOneActiveReleasePerEnvironment
```

### 3. Cell placement

```text
TestPlacement_SelectsRegionAndIsolationCompatibleCell
TestPlacement_RejectsCellWithoutCapacity
TestPlacement_IsStickyAcrossDeployments
TestPlacement_DrainingCellRejectsNewApplications
TestPlacement_ExplicitMigrationCreatesNewPlacementOperation
```

### 4. Deterministic GitOps rendering

```text
TestRenderer_SameReleaseProducesByteStableManifest
TestRenderer_ContainsDigestNotMutableTag
TestRenderer_ContainsOnlyPaaSAppAndNamespace
TestRenderer_DoesNotSerializeSecretValues
TestRenderer_PathIsCellTenantAppEnvironment
TestRenderer_GoldenFileMatchesV1alpha1Contract
```

### 5. Git commit saga

```text
TestGitOpsCommit_IdempotentByReleaseID
TestGitOpsCommit_RecordsCommitSHA
TestGitOpsCommit_DBFailureAfterPushIsRecoveredByReconciler
TestGitOpsCommit_ConcurrentDeployUsesOptimisticLock
TestRuntime_RollbackCreatesAuditableGitRevision
```

### 6. Operator reconciliation

Использовать Kubernetes `envtest`.

```text
TestOperator_CreatesDeploymentServiceAndHTTPRoute
TestOperator_SetsOwnerReferencesOnChildren
TestOperator_SetsRuntimeClassGVisorForSandboxedTier
TestOperator_SetsRestrictedSecurityContext
TestOperator_DisablesServiceAccountToken
TestOperator_SetsRequestsLimitsAndEphemeralStorage
TestOperator_CreatesDefaultDenyNetworkPolicy
TestOperator_ReconcileIsIdempotent
TestOperator_UpdatesStatusObservedGeneration
```

### 7. Rollout semantics

```text
TestRollout_ReadinessSuccessActivatesRelease
TestRollout_ReadinessFailureKeepsPreviousReleaseActive
TestRollout_MigrationRunsOncePerRelease
TestRollout_MigrationFailureBlocksTrafficSwitch
TestRollout_TimeoutMarksDeploymentDegraded
TestRollout_RetryDoesNotRepeatSuccessfulMigration
```

### 8. HPA/controller ownership

```text
TestOperator_HPAOwnsReplicaCountWhenAutoscalingEnabled
TestArgo_DoesNotManageGeneratedDeploymentReplicas
TestOperator_ManualScaleUpdatesDesiredPolicyNotChildDirectly
TestDrift_ChildMutationIsReconciledByOperator
```

### 9. Rollback

```text
TestRollback_ReusesPreviousArtifactDigest
TestRollback_DoesNotInvokeBuildPort
TestRollback_CreatesNewAuditableRelease
TestRollback_RejectsArtifactNoLongerAllowedByCriticalPolicyUnlessOverride
```

### 10. Deletion

```text
TestDelete_RemovesRouteBeforeWorkload
TestDelete_UsesRetentionStateBeforeNamespaceRemoval
TestDelete_DoesNotDeleteAttachmentReferences
TestDelete_IsRecoverableBeforeFinalPurge
TestDelete_ArgoApplicationSetPreservesResourcesOnControllerRemoval
```

### 11. Status reconciliation

```text
TestStatus_PaaSAppReadyMarksDeploymentReady
TestRuntime_ArgoSyncedWithoutHealthyWorkloadsIsNotReady
TestStatus_MissedWatchEventRecoveredByPeriodicRead
TestRuntime_UnknownObservedObjectIsQuarantined
```

## Argo contract tests

```text
TestArgoApplication_UsesRestrictedAppProject
TestArgoApplication_AutoSyncPruneSelfHealEnabled
TestArgoApplication_SourceIsOnlyCellGitOpsRepository
TestArgoApplication_DestinationIsOnlyOwnedNamespace
TestArgoAppProject_DeniesClusterScopedResources
```

## Kind acceptance-сценарий

```gherkin
Feature: Immutable runtime delivery

  Scenario: Deploy and roll back a releasable artifact
    Given a releasable image digest
    And a healthy shared runtime cell
    When an application release is created
    Then a GitOps commit is written
    And Argo CD applies a PaaSApp resource
    And the operator creates a sandboxed workload and generated route
    And the deployment becomes Ready only after application readiness succeeds
    When the release is rolled back
    Then the previous digest is deployed without a new build
```

## Порты, которые замораживаются

```go
type RuntimeStatus struct {
    DeploymentID  string
    Phase         string
    ActiveRelease string
    URL           string
    ReadyReplicas int
}

type DeploymentService interface {
    Deploy(ctx context.Context, req DeployRequest) (DeploymentRef, error)
    Rollback(ctx context.Context, environmentID, releaseID string) (DeploymentRef, error)
    Status(ctx context.Context, deploymentID string) (RuntimeStatus, error)
}
```

## Не входит в итерацию

- custom domains;
- OpenBao secrets;
- PostgreSQL/Redis provisioning;
- commercial quotas;
- agent approvals;
- multi-region traffic steering.

`attachmentSnapshotRef` использует empty adapter до итерации 5.

## Exit gate

- real kind cluster deploy green;
- operator envtest suite green;
- gVisor/security defaults validated at manifest level;
- failed rollout preserves previous release;
- rollback proves zero build calls;
- GitOps saga recovery green;
- `PaaSApp v1alpha1` schema frozen.
