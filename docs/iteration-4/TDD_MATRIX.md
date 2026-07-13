# Iteration 4 TDD matrix

Required: **63**  
Present: **63**  
Missing: **0**

| Status | Test | Implementation |
|---|---|---|
| PASS | `TestApplication_CreateBelongsToTenantProject` | `internal/runtime/application/service_test.go` |
| PASS | `TestEnvironment_DefaultProductionIsUnique` | `internal/runtime/application/service_test.go` |
| PASS | `TestEnvironment_NameIsUniqueWithinApplication` | `internal/runtime/application/service_test.go` |
| PASS | `TestEnvironment_NamespaceNameIsDeterministic` | `internal/runtime/application/service_test.go` |
| PASS | `TestEnvironment_CannotChangeTenant` | `internal/runtime/application/service_test.go` |
| PASS | `TestRelease_RejectsNonReleasableArtifact` | `internal/runtime/application/service_test.go` |
| PASS | `TestRelease_StoresExactDigest` | `internal/runtime/application/service_test.go` |
| PASS | `TestRelease_SameArtifactAndConfigIsIdempotent` | `internal/runtime/application/service_test.go` |
| PASS | `TestRelease_ConfigChangeCreatesNewRelease` | `internal/runtime/application/service_test.go` |
| PASS | `TestRelease_OnlyOneActiveReleasePerEnvironment` | `internal/runtime/application/service_test.go` |
| PASS | `TestPlacement_SelectsRegionAndIsolationCompatibleCell` | `internal/runtime/application/service_test.go` |
| PASS | `TestPlacement_RejectsCellWithoutCapacity` | `internal/runtime/application/service_test.go` |
| PASS | `TestPlacement_IsStickyAcrossDeployments` | `internal/runtime/application/service_test.go` |
| PASS | `TestPlacement_DrainingCellRejectsNewApplications` | `internal/runtime/application/service_test.go` |
| PASS | `TestPlacement_ExplicitMigrationCreatesNewPlacementOperation` | `internal/runtime/application/service_test.go` |
| PASS | `TestRenderer_SameReleaseProducesByteStableManifest` | `internal/runtime/gitops/renderer_test.go` |
| PASS | `TestRenderer_ContainsDigestNotMutableTag` | `internal/runtime/gitops/renderer_test.go` |
| PASS | `TestRenderer_ContainsOnlyPaaSAppAndNamespace` | `internal/runtime/gitops/renderer_test.go` |
| PASS | `TestRenderer_DoesNotSerializeSecretValues` | `internal/runtime/gitops/renderer_test.go` |
| PASS | `TestRenderer_PathIsCellTenantAppEnvironment` | `internal/runtime/gitops/renderer_test.go` |
| PASS | `TestRenderer_GoldenFileMatchesV1alpha1Contract` | `internal/runtime/gitops/renderer_test.go` |
| PASS | `TestGitOpsCommit_IdempotentByReleaseID` | `internal/runtime/gitops/repository_test.go` |
| PASS | `TestGitOpsCommit_RecordsCommitSHA` | `internal/runtime/gitops/repository_test.go` |
| PASS | `TestGitOpsCommit_DBFailureAfterPushIsRecoveredByReconciler` | `internal/runtime/application/gitops_saga_test.go` |
| PASS | `TestGitOpsCommit_ConcurrentDeployUsesOptimisticLock` | `internal/runtime/application/gitops_saga_test.go` |
| PASS | `TestGitOpsCommit_RevertCreatesExplicitRollbackRelease` | `internal/runtime/application/gitops_saga_test.go` |
| PASS | `TestOperator_CreatesDeploymentServiceAndHTTPRoute` | `internal/runtime/operator/reconciler_test.go` |
| PASS | `TestOperator_SetsOwnerReferencesOnChildren` | `internal/runtime/operator/reconciler_test.go` |
| PASS | `TestOperator_SetsRuntimeClassGVisorForSandboxedTier` | `internal/runtime/operator/reconciler_test.go` |
| PASS | `TestOperator_SetsRestrictedSecurityContext` | `internal/runtime/operator/reconciler_test.go` |
| PASS | `TestOperator_DisablesServiceAccountToken` | `internal/runtime/operator/reconciler_test.go` |
| PASS | `TestOperator_SetsRequestsLimitsAndEphemeralStorage` | `internal/runtime/operator/reconciler_test.go` |
| PASS | `TestOperator_CreatesDefaultDenyNetworkPolicy` | `internal/runtime/operator/reconciler_test.go` |
| PASS | `TestOperator_ReconcileIsIdempotent` | `internal/runtime/operator/reconciler_test.go` |
| PASS | `TestOperator_UpdatesStatusObservedGeneration` | `internal/runtime/operator/reconciler_test.go` |
| PASS | `TestRollout_ReadinessSuccessActivatesRelease` | `internal/runtime/operator/reconciler_test.go` |
| PASS | `TestRollout_ReadinessFailureKeepsPreviousReleaseActive` | `internal/runtime/operator/reconciler_test.go` |
| PASS | `TestRollout_MigrationRunsOncePerRelease` | `internal/runtime/operator/reconciler_test.go` |
| PASS | `TestRollout_MigrationFailureBlocksTrafficSwitch` | `internal/runtime/operator/reconciler_test.go` |
| PASS | `TestRollout_TimeoutMarksDeploymentDegraded` | `internal/runtime/operator/reconciler_test.go` |
| PASS | `TestRollout_RetryDoesNotRepeatSuccessfulMigration` | `internal/runtime/operator/reconciler_test.go` |
| PASS | `TestOperator_HPAOwnsReplicaCountWhenAutoscalingEnabled` | `internal/runtime/operator/reconciler_test.go` |
| PASS | `TestArgo_DoesNotManageGeneratedDeploymentReplicas` | `test/contract/runtime_manifests_test.go` |
| PASS | `TestOperator_ManualScaleUpdatesDesiredPolicyNotChildDirectly` | `internal/runtime/operator/reconciler_test.go` |
| PASS | `TestDrift_ChildMutationIsReconciledByOperator` | `internal/runtime/operator/reconciler_test.go` |
| PASS | `TestRollback_ReusesPreviousArtifactDigest` | `internal/runtime/application/rollback_test.go` |
| PASS | `TestRollback_DoesNotInvokeBuildPort` | `internal/runtime/application/rollback_test.go` |
| PASS | `TestRollback_CreatesNewAuditableRelease` | `internal/runtime/application/rollback_test.go` |
| PASS | `TestRollback_RejectsArtifactNoLongerAllowedByCriticalPolicyUnlessOverride` | `internal/runtime/application/rollback_test.go` |
| PASS | `TestDelete_RemovesRouteBeforeWorkload` | `internal/runtime/operator/reconciler_test.go` |
| PASS | `TestDelete_UsesRetentionStateBeforeNamespaceRemoval` | `internal/runtime/operator/reconciler_test.go` |
| PASS | `TestDelete_DoesNotDeleteAttachmentReferences` | `internal/runtime/operator/reconciler_test.go` |
| PASS | `TestDelete_IsRecoverableBeforeFinalPurge` | `internal/runtime/operator/reconciler_test.go` |
| PASS | `TestDelete_ArgoApplicationSetPreservesResourcesOnControllerRemoval` | `test/contract/runtime_manifests_test.go` |
| PASS | `TestStatus_PaaSAppReadyMarksDeploymentReady` | `internal/runtime/application/status_test.go` |
| PASS | `TestStatus_ArgoSyncedButAppNotReadyStaysDeploying` | `internal/runtime/application/status_test.go` |
| PASS | `TestStatus_MissedWatchEventRecoveredByPeriodicRead` | `internal/runtime/application/status_test.go` |
| PASS | `TestStatus_UnknownKubernetesObjectIsQuarantined` | `internal/runtime/application/status_test.go` |
| PASS | `TestArgoApplication_UsesRestrictedAppProject` | `test/contract/runtime_manifests_test.go` |
| PASS | `TestArgoApplication_AutoSyncPruneSelfHealEnabled` | `test/contract/runtime_manifests_test.go` |
| PASS | `TestArgoApplication_SourceIsOnlyCellGitOpsRepository` | `test/contract/runtime_manifests_test.go` |
| PASS | `TestArgoApplication_DestinationIsOnlyOwnedNamespace` | `test/contract/runtime_manifests_test.go` |
| PASS | `TestArgoAppProject_DeniesClusterScopedResources` | `test/contract/runtime_manifests_test.go` |
