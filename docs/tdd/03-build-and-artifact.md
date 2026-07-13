# Итерация 3. Build & Artifact Supply Chain

## Результат итерации

Платформа превращает exact `SourceRevision` в проверенный immutable OCI artifact. Релиз и Kubernetes deploy пока отсутствуют.

Основной путь:

```text
SourceRevision → detect → kpack/buildpacks → OCI digest → scan → SBOM → sign → RELEASABLE
```

Dockerfile — отдельный advanced backend через disposable VM port.

## Зависимости

- Platform Kernel contracts;
- `SourceRevision` и `SourceReader` из итерации 2.

## Владение данными

PostgreSQL schema: `build`.

Агрегаты:

- `Build`;
- `BuildAttempt`;
- `BuildPolicy`;
- `Artifact`;
- `ScanResult`;
- `SignatureRecord`;
- `BuildLogRef`.

## Invariants

1. Build всегда привязан к exact commit SHA.
2. Build identity детерминирован.
3. Одинаковый build identity может reuse существующий artifact.
4. Runtime secrets никогда не доступны build environment.
5. Успешный artifact имеет OCI digest, а не только tag.
6. Artifact не `RELEASABLE`, пока policy scan/sign не выполнены.
7. Mutable tag не меняет сохранённый artifact reference.
8. User-code failure отличается от platform failure.
9. Build cache не раскрывает данные другого tenant.
10. Cancel/supersede не создаёт частично trusted artifact.

## Build identity

```text
project_id
commit_sha
source_root
normalized_build_config_hash
builder_digest
run_image_digest
platform_build_version
```

## State machines

### Build

```text
QUEUED → FETCHING_SOURCE → DETECTING → BUILDING → EXPORTING
       → SCANNING → SIGNING → SUCCEEDED
```

Терминальные альтернативы:

```text
FAILED_USER_CODE
FAILED_PLATFORM
CANCELED
SUPERSEDED
TIMED_OUT
```

### Artifact

```text
DISCOVERED → QUARANTINED → SCANNED → SIGNED → RELEASABLE
                         ↘ REJECTED
```

## Публичные команды

```text
RequestBuild
CancelBuild
RetryBuild
GetBuild
StreamBuildLogs
EvaluateArtifact
```

## События

```text
build.requested.v1
build.started.v1
build.completed.v1
build.failed.v1
artifact.releasable.v1
artifact.rejected.v1
```

## TDD-последовательность

### 1. Build identity и deduplication

```text
TestBuildIdentity_SameInputsProduceSameIdentity
TestBuildIdentity_CommitChangeProducesNewIdentity
TestBuildIdentity_BuilderDigestChangeProducesNewIdentity
TestBuildRequest_DuplicateReusesExistingSuccessfulArtifact
TestBuildRequest_ConcurrentDuplicateCreatesOneBuild
```

### 2. Build state machine

```text
TestBuild_StartsQueued
TestBuild_AllowedTransitionMatrix
TestBuild_CannotSkipRequiredStage
TestBuild_TerminalStateIsImmutable
TestBuild_CancelIsIdempotent
TestBuild_NewerCommitCanSupersedeQueuedBuild
TestBuild_RunningBuildIsNotSupersededWithoutPolicy
```

### 3. Runtime detection

```text
TestDetector_GoModuleSelectsGoBuildpack
TestDetector_PackageJSONSelectsNodeBuildpack
TestDetector_PyprojectSelectsPythonBuildpack
TestDetector_DockerfileUsesAdvancedBackend
TestDetector_AmbiguousProjectRequiresExplicitConfig
TestDetector_UnsupportedProjectReturnsStableUserError
TestDetector_SourceRootLimitsDetectionScope
```

### 4. Source safety

```text
TestSourceFetcher_ChecksOutExactCommit
TestSourceFetcher_RejectsRepositorySizeLimit
TestSourceFetcher_RejectsSymlinkOutsideRoot
TestSourceFetcher_SubmodulesDisabledByDefault
TestSourceFetcher_RemovesCredentialBeforeBuild
TestSourceFetcher_DoesNotExposeOtherRepository
```

### 5. Secret separation

```text
TestBuild_RuntimeSecretsAreUnavailable
TestBuild_BuildSecretMountedOnlyForAllowedPhase
TestBuild_BuildSecretIsAbsentFromFinalImageEnvironment
TestBuild_BuildSecretIsAbsentFromLogs
TestBuild_BuildSecretIsAbsentFromCacheMetadata
```

### 6. Artifact trust

```text
TestArtifact_RequiresDigest
TestArtifact_TagMutationDoesNotChangeReference
TestArtifact_ScanFailureLeavesQuarantined
TestArtifact_PolicyViolationRejectsRelease
TestArtifact_SignatureRequiredForReleasable
TestArtifact_SignatureBindsExactDigest
TestArtifact_SBOMStoredByDigest
```

### 7. Failure classification

```text
TestBuild_SyntaxErrorIsUserFailure
TestBuild_DependencyRegistryTimeoutIsRetryablePlatformFailure
TestBuild_BuilderCrashIsPlatformFailure
TestBuild_TimeoutIsStableTerminalFailure
TestBuild_RetryPreservesOriginalCorrelationID
```

### 8. Cache isolation

```text
TestBuildCache_ProjectScopePreventsCrossTenantRead
TestBuildCache_TrustedBaseLayersMayBeShared
TestBuildCache_SecretMaterialIsNeverCached
TestBuildCache_PoisonedEntryIsRejectedByDigest
```

### 9. Dockerfile fallback

```text
TestDockerfileBuild_UsesIsolatedVMBackend
TestDockerfileBuild_HasNoRuntimeClusterCredential
TestDockerfileBuild_HasRestrictedEgress
TestDockerfileBuild_VMIsDestroyedAfterSuccess
TestDockerfileBuild_VMIsDestroyedAfterFailure
```

## Adapter contracts

### kpack

```text
TestKpackAdapter_CreatesBuildForExactSourceRevision
TestKpackAdapter_MapsLifecycleStatus
TestKpackAdapter_CollectsImageDigest
TestKpackAdapter_CancelDeletesOnlyOwnedBuild
```

### Harbor/registry

```text
TestRegistryAdapter_PushesToTenantRepository
TestRegistryAdapter_ResolvesDigest
TestRegistryAdapter_DeniesCrossTenantRepository
TestRegistryAdapter_StoresSBOMAndSignatureReferences
```

### Scanner/signer

```text
TestScannerAdapter_MapsPolicyThresholds
TestSignerAdapter_SignsDigestNotTag
TestVerifier_RejectsUnknownIssuer
```

## Integration fixtures

Обязательные sample projects:

```text
hello-go
hello-node
hello-python
invalid-node
ambiguous-monorepo
malicious-symlink
dockerfile-safe
```

## Acceptance-сценарий

```gherkin
Feature: Source-to-artifact

  Scenario: Build a Go repository into a releasable artifact
    Given a ready project with an exact Go commit
    When a build is requested twice with the same idempotency key
    Then only one build executes
    And the build succeeds with an immutable OCI digest
    And an SBOM and signature are attached to that digest
    And the artifact is marked RELEASABLE
    And no runtime secret appears in build logs or metadata
```

## Порты, которые замораживаются

```go
type ArtifactRef struct {
    ArtifactID string
    Repository string
    Digest     string
    MediaType  string
}

type ArtifactPolicy interface {
    IsReleasable(ctx context.Context, artifact ArtifactRef) (ReleasabilityDecision, error)
}
```

## Не входит в итерацию

- application/environment;
- Argo CD;
- Kubernetes;
- release promotion;
- service bindings;
- billing of build usage.

Build usage events можно записывать, но rating появится в итерации 6.

## Exit gate

- Go/Node/Python fixtures строятся в OCI digest;
- scan/sign policy test green;
- secret leakage suite green;
- duplicate build test green под concurrency;
- Dockerfile backend boundary test green;
- `ArtifactRef` frozen as v1.
