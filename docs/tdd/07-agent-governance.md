# Итерация 7. Agent Governance & Public API

## Результат итерации

AI-агент получает безопасную MCP/API surface и может провести полный lifecycle приложения, не имея прямого доступа к GitLab admin, Kubernetes, Argo CD, Harbor, OpenBao или Cozystack.

Этот домен не владеет project/build/runtime/service data. Он оркестрирует только опубликованные контракты предыдущих bounded contexts.

## Зависимости

- Kernel authorization/operations/audit;
- Source Control API;
- Build API;
- Runtime Delivery API;
- Attachments API;
- Commercial Governance API.

## Владение данными

PostgreSQL schema: `agent`.

Агрегаты:

- `AgentPrincipal`;
- `AgentSession`;
- `AgentTask`;
- `ActionBudget`;
- `ApprovalRequest`;
- `ApprovalGrant`;
- `RepairLoop`;
- `ToolInvocationRecord`.

## Invariants

1. Agent действует от собственного principal и `on_behalf_of` user.
2. Scope проверяется server-side для каждого tool invocation.
3. Agent не получает provider admin credentials.
4. Agent может записать secret, но не прочитать value обратно.
5. Approval одноразовый, ограничен actor/action/resource/payload hash и expiry.
6. Destructive actions без approval запрещены.
7. Build/deploy budgets ограничивают autonomous loops.
8. Одинаковая ошибка, повторённая сверх threshold, ставит repair loop на pause.
9. Все tool calls имеют correlation/task ID и audit trail.
10. Повтор MCP request с тем же idempotency key не дублирует side effect.
11. Agent не может обойти commercial entitlement.
12. Prompt text не влияет на authorization decision.

## Public MCP tools v1

```text
platform_create_project
platform_get_project
platform_apply_repository_patch
platform_create_branch
platform_create_merge_request
platform_request_build
platform_get_build
platform_deploy
platform_get_deployment
platform_rollback
platform_set_secret
platform_list_secret_metadata
platform_provision_service
platform_bind_service
platform_add_domain
platform_get_logs
platform_get_usage
platform_request_approval
platform_get_operation
platform_cancel_operation
```

Намеренно отсутствуют:

```text
kubectl
helm
argocd_admin
vault_read_secret
cozystack_raw_apply
gitlab_admin_token
harbor_admin
```

## Approval-required actions v1

Обязательный approval:

- delete production application;
- purge managed database;
- transfer custom domain;
- increase paid plan/limits;
- deploy to production, если tenant policy требует;
- disable backups;
- expose SMTP/privileged network capability.

## TDD-последовательность

### 1. Agent identity and scopes

```text
TestAgentPrincipal_IsDistinctFromUserPrincipal
TestAgentPrincipal_RecordsOnBehalfOfUser
TestAgentScope_AllowsExplicitTool
TestAgentScope_DeniesUnlistedTool
TestAgentScope_CannotEscalateViaToolArguments
TestAgentScope_CrossTenantResourceDenied
```

### 2. MCP schema contracts

```text
TestMCPToolSchemas_AreVersionedAndStable
TestMCPToolSchemas_RejectUnknownRequiredSemantics
TestMCPToolSchemas_ValidateIdentifiersAndLimits
TestMCPResponse_AlwaysIncludesOperationOrResultReference
TestMCPError_UsesStablePublicErrorContract
```

Golden files хранить в `/contracts/mcp/v1`.

### 3. Provider credential boundary

```text
TestAgentResponse_NeverContainsGitLabAdminToken
TestAgentResponse_NeverContainsKubernetesCredential
TestAgentResponse_NeverContainsOpenBaoToken
TestAgentLogs_RedactRepositoryWriteCredential
TestAgentWorkspaceCredentialExpiresAfterOperation
```

### 4. Secret write-only behavior

```text
TestAgent_CanSetSecretWithScope
TestAgent_CannotReadSecretValue
TestAgent_ListSecretsReturnsMetadataOnly
TestAgent_SecretValueAbsentFromToolInvocationAudit
TestAgent_SecretValueAbsentFromErrorOnProviderFailure
```

### 5. Approval lifecycle

```text
TestApproval_RequestContainsActionResourceAndPayloadHash
TestApproval_GrantAllowsExactlyMatchingAction
TestApproval_GrantCannotBeReused
TestApproval_GrantExpires
TestApproval_GrantCannotBeUsedByDifferentAgent
TestApproval_GrantCannotBeUsedForDifferentPayload
TestApproval_DenialLeavesNoSideEffect
```

### 6. Action budgets

```text
TestBudget_BuildCountLimitStopsAdditionalBuild
TestBudget_BuildMinutesLimitStopsLongLoop
TestBudget_DeployCountLimitStopsRepeatedDeploy
TestBudget_ResetRequiresNewTaskOrExplicitPolicy
TestBudget_UsageIsAtomicUnderConcurrentToolCalls
```

### 7. Repair-loop control

```text
TestRepairLoop_SameFailureFingerprintIncrementsCounter
TestRepairLoop_DifferentFailureResetsOrBranchesCounterByPolicy
TestRepairLoop_ThresholdPausesAutonomy
TestRepairLoop_PauseRequiresUserDecision
TestRepairLoop_PlatformFailureDoesNotConsumeUserRepairBudget
```

### 8. Orchestration and idempotency

```text
TestAgentCreateProject_ReplayReturnsSameProject
TestAgentBuild_WaitsThroughOperationContractNotInternalPolling
TestAgentDeploy_UsesArtifactReturnedByBuild
TestAgentProvisionAndBind_UsesPublishedAttachmentContracts
TestAgentCancellation_PropagatesToCancelableOperation
TestAgentConcurrentDeploys_UseExpectedEnvironmentRevision
```

### 9. Commercial enforcement

```text
TestAgentAction_EntitlementCheckedBeforeSideEffect
TestAgentAction_QuotaRejectionIsExplainedWithoutInternalData
TestAgentAction_CannotApproveOwnPaidUpgrade
TestAgentAction_SuspendedTenantCanReadStatusButCannotMutate
```

### 10. Audit and traceability

```text
TestAgentAudit_ContainsTaskToolActorUserAndCorrelation
TestAgentAudit_ConnectsCommitBuildReleaseDeployment
TestAgentAudit_RedactsSecretsAndTokens
TestAgentAudit_FailedAuthorizationRecorded
TestAgentAudit_OperationReplayReferencesOriginalInvocation
```

### 11. Fuzz/security tests

```text
FuzzMCPToolArguments_NoPanicNoScopeEscalation
FuzzRepositoryPatch_NoPathEscape
FuzzApprovalPayloadHash_NoConfusedDeputy
FuzzPublicErrors_NoSecretLeak
```

### 12. Chaos/recovery tests

```text
TestAgentFlow_LostGitLabWebhookRecoveredBySourceReconciler
TestAgentFlow_BuildProviderTimeoutResumesOperation
TestAgentFlow_GitOpsCommitResponseLostIsReconciled
TestAgentFlow_ArgoUnavailableLeavesDeploymentPendingNotDuplicated
TestAgentFlow_StatusEventLostRecoveredByRuntimeReconciler
```

## Full acceptance scenario

```gherkin
Feature: AI-native application lifecycle

  Scenario: Agent creates, builds and deploys a production application safely
    Given an organization owner authorizes an agent for project and staging deployment
    When the agent creates project "booking"
    And commits a Go service
    And requests a build
    Then the build returns one releasable immutable artifact
    When the agent provisions PostgreSQL and binds it to production
    And writes the required runtime secret
    And requests a production deployment
    Then the platform requests approval if tenant policy requires it
    When the user grants the matching approval
    Then the deployment becomes Ready at a generated URL
    And the agent can read status and redacted logs
    But the agent cannot read secret values or provider credentials
    And the audit trail connects task, commit, build, release and deployment
```

## Negative end-to-end scenarios

```text
Agent tries to deploy across tenant boundary → denied before build/deploy
Agent retries same create command → same project, no duplicate GitLab repo
Agent attempts DB purge without approval → denied, DB remains Ready
Agent submits altered payload with old approval → denied
Agent enters repeated failing build loop → paused at policy threshold
Tenant quota exhausted → no Kubernetes mutation occurs
```

## Public API compatibility gate

Перед release:

- MCP JSON schema diff reviewed;
- OpenAPI diff reviewed;
- all prior domain consumer contract tests green;
- no direct import of internal packages from previous domains;
- one full system scenario on disposable environment green;
- security redaction/fuzz suite green.

## Exit gate

Продукт готов к controlled public beta, когда:

- full agent lifecycle green;
- destructive action approval suite green;
- budget/repair-loop tests green;
- no secret/provider credential leakage;
- lost-event chaos suite green;
- commercial quota blocks before side effects;
- MCP v1 contracts frozen.
