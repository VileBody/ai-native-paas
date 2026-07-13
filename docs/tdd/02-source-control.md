# Итерация 2. Source Control

## Результат итерации

Платформа умеет программно создать tenant project и GitLab repository, безопасно принимать изменения от AI-агента, наблюдать commits/MR и восстанавливать пропущенные webhook events.

Build и deploy здесь отсутствуют. Конечный результат — стабильный `SourceRevision`.

## Зависимости

Используются только опубликованные контракты итерации 1:

- `PrincipalContext`;
- `TenantRef`;
- `OperationRef`;
- authorization;
- idempotency;
- outbox/audit.

## Владение данными

PostgreSQL schema: `source`.

Агрегаты:

- `Project`;
- `Repository`;
- `BranchHead`;
- `MergeRequest`;
- `SourceEventReceipt`;
- `AgentWorkspace`.

Provider-specific identifiers:

- GitLab numeric group ID;
- GitLab numeric project ID;
- GitLab hook ID.

Path/slug не являются primary linkage.

## Invariants

1. Project принадлежит ровно одной organization.
2. Repository provisioning idempotent.
3. Пользовательский или agent token никогда не имеет admin scope.
4. Source revision всегда содержит exact commit SHA.
5. Duplicate webhook не создаёт duplicate source event.
6. Out-of-order webhook не откатывает branch head назад.
7. Пропущенный webhook восстанавливается reconciler-ом.
8. Agent patch не может выйти за repository root.
9. Commit attribution содержит user/agent identity и correlation ID.
10. Default branch защищена согласно platform policy.

## State machines

### Repository

```text
REQUESTED → PROVISIONING → READY
                 ↘ FAILED_RETRYABLE
                 ↘ FAILED_FINAL
READY → ARCHIVING → ARCHIVED
```

### Agent workspace

```text
CREATED → CHECKED_OUT → PATCHED → VALIDATED → COMMITTED → PUSHED → DESTROYED
                       ↘ REJECTED
```

## Публичные команды

```text
CreateProject
ProvisionRepository
CreateBranch
ApplyRepositoryPatch
CommitChanges
CreateMergeRequest
ArchiveProject
ReconcileRepository
```

## События

```text
source.project_created.v1
source.repository_ready.v1
source.commit_observed.v1
source.merge_request_changed.v1
source.repository_archived.v1
```

`source.commit_observed.v1` содержит:

```json
{
  "project_id": "prj_...",
  "repository_id": "repo_...",
  "branch": "main",
  "commit_sha": "...",
  "author_kind": "agent",
  "author_id": "agent_...",
  "observed_at": "..."
}
```

## TDD-последовательность

### 1. Project aggregate

```text
TestProject_Create_BelongsToTenant
TestProject_Create_RejectsDuplicateSlugWithinTenant
TestProject_SameSlugAllowedAcrossTenants
TestProject_RenameDoesNotChangeProviderIdentity
TestProject_ArchiveIsTwoPhase
```

### 2. Repository provisioning saga

```text
TestRepository_ProvisioningStartsRequested
TestRepository_ProvisioningStoresNumericGitLabIDs
TestRepository_ProvisioningIsIdempotent
TestRepository_GitLabTimeoutLeavesRetryableState
TestRepository_RetryReusesExistingGitLabProject
TestRepository_PartialGroupCreationIsReconciled
TestRepository_DefaultBranchPolicyIsApplied
```

### 3. Webhook ingestion

```text
TestWebhook_RejectsInvalidSignature
TestWebhook_DuplicateDeliveryIsIgnored
TestWebhook_PushCreatesCommitObservedEvent
TestWebhook_OutOfOrderPushDoesNotRegressBranchHead
TestWebhook_UnknownProjectIsQuarantined
TestWebhook_DeletedBranchMarksHeadDeleted
TestWebhook_MergeRequestLifecycleIsNormalized
```

### 4. Source reconciler

```text
TestReconciler_DetectsMissedCommit
TestReconciler_NoChangeProducesNoEvent
TestReconciler_ProviderTemporaryFailureRetries
TestReconciler_ArchivedProjectIsSkipped
TestReconciler_UsesNumericProjectIDAfterRename
```

### 5. Agent workspace

```text
TestWorkspace_ApplyPatchRejectsPathTraversal
TestWorkspace_ApplyPatchRejectsSymlinkEscape
TestWorkspace_RejectsOversizedPatch
TestWorkspace_CommitUsesAgentAttribution
TestWorkspace_CommitIncludesCorrelationTrailer
TestWorkspace_CredentialsExpireAfterPush
TestWorkspace_DestroyRemovesCredentialsAndCheckout
TestWorkspace_ConcurrentPatchUsesExpectedBaseSHA
```

### 6. Token boundary

```text
TestGitCredential_HasRepositoryOnlyScope
TestGitCredential_HasFiniteExpiry
TestGitCredential_CannotReadOtherTenantRepository
TestGitCredential_IsNeverSerializedInAuditOrError
```

## Adapter contract: GitLab

HTTP stub contract tests:

```text
TestGitLabAdapter_CreateGroupRequest
TestGitLabAdapter_CreateProjectInNumericNamespace
TestGitLabAdapter_FindExistingProjectOnConflict
TestGitLabAdapter_ProtectDefaultBranch
TestGitLabAdapter_CreateScopedAccessToken
TestGitLabAdapter_ReadBranchHead
TestGitLabAdapter_CreateMergeRequest
TestGitLabAdapter_MapsRateLimitToRetryableError
```

Nightly real-provider tests:

```text
TestGitLabReal_ProvisionCommitObserveArchive
TestGitLabReal_ProjectRenameKeepsNumericIdentity
TestGitLabReal_TokenCannotAccessSiblingProject
```

## Acceptance-сценарий

```gherkin
Feature: AI-managed source repository

  Scenario: Agent creates and updates a tenant project
    Given an organization owner
    When the owner creates project "booking"
    Then a private GitLab repository is provisioned
    And the default branch is protected
    When an authorized agent applies a patch and commits it
    Then the commit is attributed to the agent on behalf of the user
    And the platform emits one source.commit_observed.v1 event
    When the same webhook is delivered twice
    Then no duplicate source event is emitted
```

## Порты, которые замораживаются

```go
type SourceRevision struct {
    ProjectID    string
    RepositoryID string
    Branch       string
    CommitSHA    string
    SourceRoot   string
}

type SourceReader interface {
    ResolveRevision(ctx context.Context, projectID, ref string) (SourceRevision, error)
    FetchArchive(ctx context.Context, revision SourceRevision, dst io.Writer) error
}
```

## Не входит в итерацию

- runtime detection;
- build queue;
- image registry;
- deployment;
- secret injection;
- user-defined CI as deployment engine.

## Exit gate

- full project→repo→commit acceptance green;
- duplicate/out-of-order webhook suite green;
- missed-webhook reconciler green;
- path traversal and credential leakage tests green;
- `SourceRevision` и source events frozen as v1.
