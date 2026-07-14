# Agentic DevOps pivot — TDD evidence matrix

Generated from `docs/pivot/tdd-catalog.json`. Do not edit by hand.

- Pivot requirements: **157**
- Discovered Go tests/fuzz targets: **989**
- Reused now: **100**
- New local/contract tests required: **0**
- Live/provider/system tests required: **57**
- Unmapped: **0**

`NEW` and `LIVE_ONLY` are explicit implementation work, not passing evidence.
A release gate may become green only after every referenced test exists and passes.

| ID | Status | Level | Executable evidence target |
|---|---|---|---|
| `K1` | REUSED | domain + application | `internal/kernel/execution/execution_tdd_test.go::TestKernel_ProjectScopedPrincipalCannotCrossProject` |
| `K2` | REUSED | domain | `internal/kernel/execution/execution_tdd_test.go::TestKernel_WorkspacePrincipalCannotEscalateToTenantScope` |
| `K3` | REUSED | domain + fake clock | `internal/kernel/execution/execution_tdd_test.go::TestKernel_CredentialLeaseExpiresAndCannotBeReused` |
| `K4` | REUSED | application | `internal/kernel/execution/execution_tdd_test.go::TestKernel_OperationWaitsForApprovalWithoutRepeatingSideEffect` |
| `K5` | REUSED | application | `internal/kernel/execution/execution_tdd_test.go::TestKernel_ParentCancellationPropagatesToCancelableChildren` |
| `K6` | REUSED | application + concurrency | `internal/kernel/execution/execution_tdd_test.go::TestKernel_IdempotentCommandReturnsOriginalOperationGraph` |
| `K7` | REUSED | domain | `internal/kernel/execution/execution_tdd_test.go::TestKernel_IdempotencyPayloadMismatchCannotReuseApproval` |
| `K8` | REUSED | PostgreSQL + broker integration | `test/integration/postgres_kernel_test.go::TestKernel_OutboxCommitThenCrashPublishesExactlyOnceEffect` |
| `K9` | REUSED | application | `internal/security/redact/redact_tdd_test.go::TestKernel_AuditRedactsWorkspaceCommandSecrets` |
| `K10` | REUSED | domain | `internal/kernel/execution/execution_tdd_test.go::TestKernel_ServicePrincipalCannotApproveHumanAction` |
| `K11` | REUSED | HTTP integration | `internal/identity/httpauth/middleware_test.go::TestKernel_ProductionProfileRejectsDevelopmentIdentityHeaders` |
| `K12` | REUSED | identity provider integration | `internal/identity/httpauth/middleware_test.go::TestKernel_OIDCAndMTLSIdentityCannotBeConfused` |
| `K13` | REUSED | live PostgreSQL | `test/integration/postgres_migration_lock_test.go::TestPostgres_ConcurrentMigrationStartupUsesOneOwner` |
| `K14` | LIVE_ONLY | chaos + PostgreSQL | `test/pivot/kernel_v2_test.go::TestKernel_OperationCheckpointSurvivesDatabaseFailover` |
| `S1` | REUSED | application + GitLab contract | `test/pivot/source_v2_test.go::TestSource_CreateProjectBootstrapsV2RepositoryLayout` |
| `S2` | REUSED | real local Git integration | `internal/workspaceagent/source_v2_test.go::TestSource_CheckoutUsesExactCommitNotMutableBranchHead` |
| `S3` | REUSED | fuzz + application | `internal/workspaceagent/source_v2_test.go::TestSource_PatchRejectsNonCanonicalAndGitInternalPaths` |
| `S4` | REUSED | application | `internal/workspace/service_tdd_test.go::TestSource_ProductionGitOpsPathRequiresApprovalPolicy` |
| `S5` | REUSED | Git integration | `internal/workspaceagent/source_v2_test.go::TestSource_AgentCommitContainsSignedAttestationAndCorrelation` |
| `S6` | REUSED | Git integration + concurrency | `internal/workspaceagent/source_v2_test.go::TestSource_ConcurrentPushUsesExpectedBaseSHA` |
| `S7` | REUSED | application | `internal/workspaceagent/source_v2_test.go::TestSource_SecretScannerBlocksCredentialBeforeCommit` |
| `S8` | LIVE_ONLY | real GitLab provider | `test/pivot/source_v2_test.go::TestSource_ProjectTokenCannotReadSiblingRepository` |
| `S9` | REUSED | application + provider fake | `test/pivot/source_v2_test.go::TestSource_MissedWebhookRecoveredByBranchReconciler` |
| `S10` | REUSED | application | `test/pivot/source_v2_test.go::TestSource_OutOfOrderWebhookCannotRegressObservedHead` |
| `S11` | LIVE_ONLY | real GitLab provider | `test/pivot/source_v2_test.go::TestSource_RenameKeepsNumericProviderIdentity` |
| `S12` | REUSED | GitLab contract | `test/pivot/source_v2_test.go::TestSource_MergeRequestPublishesPlanSummaryWithoutSecrets` |
| `S13` | REUSED | application | `test/pivot/source_v2_test.go::TestSource_DeletedBranchProducesEnvironmentCleanupIntent` |
| `S14` | REUSED | integration | `internal/workspaceagent/source_v2_test.go::TestSource_SubmoduleAndLFSFollowExplicitPolicy` |
| `S15` | REUSED | provider contract | `test/pivot/source_v2_test.go::TestSource_GitLab429UsesBoundedRetryAndPreservesIdempotency` |
| `S16` | REUSED | application + provider | `test/pivot/source_v2_test.go::TestSource_ProjectArchiveIsTwoPhaseAndReversibleBeforePurge` |
| `S17` | REUSED | real Git integration | `internal/workspaceagent/source_v2_test.go::TestSource_CheckoutCredentialRemovedFromDiskAndGitConfig` |
| `S18` | REUSED | reconciliation | `test/pivot/source_v2_test.go::TestSource_UnknownProviderProjectIsQuarantinedNotAdopted` |
| `B1` | REUSED | domain | `test/pivot/build_v2_test.go::TestBuild_IdentityIncludesCommitAndCanonicalBuildSpec` |
| `B2` | REUSED | application | `internal/build/application/service_test.go::TestBuild_ExplicitBuildSpecOverridesRuntimeDetection` |
| `B3` | REUSED | application | `internal/build/application/service_test.go::TestBuild_BuildpacksRemainOptionalFallback` |
| `B4` | REUSED | application | `internal/build/application/service_test.go::TestBuild_DockerfileRunsOnlyInDisposableIsolationBackend` |
| `B5` | LIVE_ONLY | provider/system security | `test/pivot/build_v2_test.go::TestBuild_NetworkProfileBlocksMetadataPrivateAndControlPlane` |
| `B6` | LIVE_ONLY | system | `test/pivot/build_v2_test.go::TestBuild_ResourceLimitsTerminateForkBombAndOversizedContext` |
| `B7` | REUSED | integration | `internal/build/provenance/security_integration_test.go::TestBuild_SecretsNeverEnterLayerLogOrProvenance` |
| `B8` | REUSED | integration | `internal/build/cache/cache_test.go::TestBuild_CacheIsProjectScopedForUntrustedLayers` |
| `B9` | REUSED | registry integration | `internal/build/registry/local_test.go::TestBuild_RegistryPushResponseLostRecoversByDigestDiscovery` |
| `B10` | REUSED | domain + PostgreSQL | `test/integration/postgres_build_test.go::TestBuild_TrustChainRequiredBeforeArtifactReleasable` |
| `B11` | REUSED | policy | `internal/build/dockerfilepolicy/policy_test.go::TestBuild_MutableBaseOrOutputTagCannotDefineProductionArtifact` |
| `B12` | REUSED | PostgreSQL + concurrency | `test/integration/postgres_build_test.go::TestBuild_SameIdentityConcurrentRequestsExecuteOnce` |
| `B13` | LIVE_ONLY | application/system | `test/pivot/build_v2_test.go::TestBuild_CancelStopsExecutionAndRevokesBuildCredentials` |
| `B14` | REUSED | registry integration | `internal/build/multiarch/multiarch_test.go::TestBuild_MultiArchManifestContainsOnlyVerifiedPlatformDigests` |
| `B15` | REUSED | contract/crypto | `internal/build/provenance/provenance_test.go::TestBuild_ProvenanceBindsSourceSpecBuilderAndOutputDigest` |
| `R1` | REUSED | application | `internal/runtime/gitops/policy_test.go::TestGitOps_CommitMayModifyOnlyProjectEnvironmentPath` |
| `R2` | REUSED | integration/golden | `test/pivot/runtime_gitops_helm_test.go::TestGitOps_HelmRenderIsDeterministicForPinnedInputs` |
| `R3` | REUSED | integration/fuzz | `internal/runtime/gitops/kustomize_test.go::TestGitOps_KustomizeRenderIsDeterministicAndPathSafe` |
| `R4` | REUSED | policy | `internal/runtime/gitops/policy_test.go::TestGitOps_ForbiddenClusterScopedResourceRejectedBeforeCommit` |
| `R5` | REUSED | policy | `internal/runtime/gitops/policy_test.go::TestGitOps_PrivilegedWorkloadRejectedRegardlessOfHelmSource` |
| `R6` | LIVE_ONLY | manifest contract + real Argo | `test/pivot/runtime_gitops_test.go::TestArgoProject_AllowsOnlyExpectedRepositoryClusterAndNamespaces` |
| `R7` | LIVE_ONLY | application/system | `test/pivot/runtime_gitops_test.go::TestRuntime_ArgoSyncedWithoutHealthyWorkloadsIsNotReady` |
| `R8` | REUSED | application + Git | `internal/runtime/application/gitops_saga_test.go::TestRuntime_RollbackCreatesAuditableGitRevision` |
| `R9` | LIVE_ONLY | system | `test/pivot/runtime_gitops_test.go::TestRuntime_DriftIsReportedAndSelfHealFollowsPolicy` |
| `R10` | LIVE_ONLY | Kubernetes system | `test/pivot/runtime_gitops_test.go::TestRuntime_NamespaceAndServiceAccountIsolation` |
| `R11` | LIVE_ONLY | system | `test/pivot/runtime_gitops_test.go::TestRuntime_HPAAndGitOpsDoNotFightOverReplicas` |
| `R12` | REUSED | policy | `internal/attachments/recipe/registry_test.go::TestRuntime_ProductOperatorCRAllowedOnlyByRecipePolicy` |
| `R13` | REUSED | operator contract | `test/pivot/runtime_gitops_test.go::TestRuntime_PaaSAppFastPathProducesEquivalentStandardResources` |
| `R14` | LIVE_ONLY | real Argo/ApplicationSet | `test/pivot/runtime_gitops_test.go::TestRuntime_PreviewApplicationSetCreatedAndRemovedFromBranchLifecycle` |
| `R15` | LIVE_ONLY | Kubernetes/Gateway system | `test/pivot/runtime_gitops_test.go::TestRuntime_FailedNewRevisionKeepsPreviousTrafficWhereStrategySupportsIt` |
| `R16` | REUSED | reconciliation | `internal/runtime/application/status_test.go::TestRuntime_UnknownObservedObjectIsQuarantined` |
| `R17` | LIVE_ONLY | real Kubernetes | `test/pivot/runtime_gitops_test.go::TestRuntime_GVisorRequiredForSharedUntrustedTier` |
| `R18` | LIVE_ONLY | real Kubernetes/network | `test/pivot/runtime_gitops_test.go::TestRuntime_CiliumDefaultDenyAndExplicitEgressProfiles` |
| `A5.1` | REUSED | application | `internal/attachments/application/attachments_tdd_test.go::TestSecret_SetIsWriteOnlyAndReturnsMetadataOnly` |
| `A5.2` | REUSED | domain + OpenBao integration | `internal/attachments/openbao/project_isolation_test.go::TestSecret_ProjectAndEnvironmentScopesAreIsolated` |
| `A5.3` | REUSED | PostgreSQL + redaction | `test/integration/postgres_attachments_test.go::TestSecret_ValueAbsentFromControlPlanePersistenceAndEvents` |
| `A5.4` | LIVE_ONLY | credential broker integration | `test/pivot/attachments_v2_test.go::TestCredential_LeaseIsShortLivedProjectScopedAndSinglePurpose` |
| `A5.5` | LIVE_ONLY | application/system | `test/pivot/attachments_v2_test.go::TestCredential_RotationCutsOverBeforeRevokingOldVersion` |
| `A5.6` | REUSED | OpenBao adapter | `internal/attachments/openbao/httpbackend_test.go::TestCredential_RevokePrefixFailsClosedWhenBackendCannotGuaranteeIt` |
| `A5.7` | LIVE_ONLY | cross-domain acceptance | `test/pivot/attachments_v2_test.go::TestSecret_GitWorkspacePlanAndBuildNeverContainValue` |
| `A5.8` | REUSED | domain + PostgreSQL + crypto | `test/integration/postgres_attachments_test.go::TestRecipe_ActivatedVersionIsImmutableAndSigned` |
| `A5.9` | REUSED | application | `internal/attachments/recipe/registry_test.go::TestRecipe_ResolutionPinsExactVersionAndArtifactDigests` |
| `A5.10` | REUSED | domain | `internal/attachments/recipe/registry_test.go::TestRecipe_DependencyGraphRejectsCyclesAndVersionConflict` |
| `A5.11` | REUSED | application | `internal/attachments/recipe/registry_test.go::TestRecipe_PlanIsPureAndCreatesNoExternalResource` |
| `A5.12` | REUSED | policy integration | `internal/attachments/recipe/registry_test.go::TestRecipe_DeclaredPermissionsMatchRenderedResources` |
| `A5.13` | REUSED | contract | `internal/attachments/recipe/registry_test.go::TestRecipe_HealthBackupUpgradeAndRemovalProceduresAreComplete` |
| `A5.14` | REUSED | application | `internal/attachments/recipe/registry_test.go::TestRecipe_CustomAdHocInstallIsAllowedWithinStricterPolicy` |
| `A5.15` | LIVE_ONLY | cross-domain application | `test/pivot/attachments_v2_test.go::TestProviderResource_EntitlementAndReservationPrecedeApply` |
| `A5.16` | LIVE_ONLY | provider contract | `test/pivot/attachments_v2_test.go::TestProviderResource_LostApplyResponseRecoveredByExternalIdentity` |
| `A5.17` | LIVE_ONLY | application | `test/pivot/attachments_v2_test.go::TestProviderResource_ObservedReadyCreatesBindingsAndUsageOnce` |
| `A5.18` | LIVE_ONLY | application/provider | `test/pivot/attachments_v2_test.go::TestProviderResource_DeletePolicyRetainDoesNotDestroyData` |
| `A5.19` | REUSED | governance | `test/pivot/attachments_v2_test.go::TestProviderResource_DestroyRequiresMatchingPlanHashApproval` |
| `A5.20` | LIVE_ONLY | provider/system | `test/pivot/attachments_v2_test.go::TestProviderResource_FinalBackupCompletesBeforeApprovedPurge` |
| `A5.21` | LIVE_ONLY | gateway integration | `test/pivot/attachments_v2_test.go::TestCapability_OpenRouterTokenIsProjectScopedAndMasterKeyHidden` |
| `A5.22` | LIVE_ONLY | commerce/gateway | `test/pivot/attachments_v2_test.go::TestCapability_BudgetExceededStopsRequestBeforeProviderCall` |
| `A5.23` | LIVE_ONLY | integration | `test/pivot/attachments_v2_test.go::TestCapability_ProviderUsageIsAttributedAndDeduplicated` |
| `A5.24` | LIVE_ONLY | contract | `test/pivot/attachments_v2_test.go::TestCapability_ProviderSubstitutionPreservesPlatformContract` |
| `A5.25` | LIVE_ONLY | security | `test/pivot/attachments_v2_test.go::TestCapability_ApifyOrBrightDataCredentialCannotBeUsedOutsideGateway` |
| `A5.26` | REUSED | concurrency | `test/pivot/attachments_v2_test.go::TestCapability_RateLimitIsPerProjectAndDoesNotLeakCrossTenantState` |
| `A5.27` | REUSED | domain + PostgreSQL | `test/integration/postgres_attachments_test.go::TestInputsSnapshot_IsImmutableAndContainsReferencesOnly` |
| `A5.28` | REUSED | cross-domain | `internal/runtime/application/gitops_saga_test.go::TestInputsSnapshot_SameImageNewInputsCreatesNewRuntimeRevision` |
| `A5.29` | REUSED | cross-domain | `internal/runtime/application/gitops_saga_test.go::TestInputsSnapshot_RollbackRestoresMatchingHistoricalInputs` |
| `A5.30` | LIVE_ONLY | DNS provider integration | `test/pivot/attachments_v2_test.go::TestDomain_OwnershipProofRequiresIndependentDNSObservations` |
| `A5.31` | LIVE_ONLY | provider/runtime integration | `test/pivot/attachments_v2_test.go::TestDomain_RouteActivatesOnlyAfterOwnershipAndTLSReady` |
| `A5.32` | REUSED | domain + fake clock | `test/pivot/attachments_v2_test.go::TestDomain_ReleaseEntersQuarantineBeforeAnotherTenantCanClaim` |
| `C1` | REUSED | application/golden | `test/pivot/commerce_v2_test.go::TestCost_TofuPlanProducesDeterministicNormalizedEstimate` |
| `C2` | LIVE_ONLY | domain | `test/pivot/commerce_v2_test.go::TestCost_UnknownProviderPriceProducesRangeAndApprovalRequirement` |
| `C3` | LIVE_ONLY | application | `test/pivot/commerce_v2_test.go::TestCost_HelmResourcesContributeRequestedRuntimeAllocation` |
| `C4` | REUSED | domain | `test/pivot/commerce_v2_test.go::TestCost_ApprovalInvalidatedWhenPlanHashChanges` |
| `C5` | LIVE_ONLY | cross-domain application | `test/pivot/commerce_v2_test.go::TestQuota_ReservationCommittedBeforeExternalApply` |
| `C6` | REUSED | PostgreSQL + race | `test/integration/postgres_commerce_test.go::TestQuota_ConcurrentPlansCannotOversubscribeProjectBudget` |
| `C7` | REUSED | domain/fake clock | `test/pivot/commerce_v2_test.go::TestQuota_ExpiredReservationCannotAuthorizeLateApply` |
| `C8` | LIVE_ONLY | application/provider | `test/pivot/commerce_v2_test.go::TestUsage_PartialApplySettlesCreatedResourcesAndReleasesRemainder` |
| `C9` | LIVE_ONLY | integration | `test/pivot/commerce_v2_test.go::TestUsage_ProviderReportReplayDoesNotDoubleCharge` |
| `C10` | LIVE_ONLY | capability gateway integration | `test/pivot/commerce_v2_test.go::TestUsage_OpenRouterTokensAreAttributedToProjectTaskAndModel` |
| `C11` | REUSED | rating | `internal/commerce/application/commercial_tdd_test.go::TestUsage_ApifyAndBrightDataMetersRemainProviderSpecificButInvoiceStable` |
| `C12` | REUSED | workspace/commerce integration | `test/pivot/commerce_v2_test.go::TestBudget_WorkspaceCommandStoppedBeforeExceedingHardLimit` |
| `C13` | REUSED | Agent/Commerce | `internal/agent/application/agent_tdd_test.go::TestBudget_RepairLoopConsumesConfiguredNotUnlimitedBudget` |
| `C14` | LIVE_ONLY | cross-domain | `test/pivot/commerce_v2_test.go::TestCommercial_SuspendedProjectIsReadOnlyAndRetainsData` |
| `C15` | REUSED | property/fuzz | `internal/commerce/application/commercial_tdd_test.go::TestRating_UsesExactArithmeticAcrossMicroUsageAndLargeQuantities` |
| `C16` | REUSED | contract/golden | `test/pivot/commerce_v2_test.go::TestCost_ApprovalSummaryIncludesDestructionRiskAndMonthlyDelta` |
| `C17` | LIVE_ONLY | reconciliation | `test/pivot/commerce_v2_test.go::TestUsage_ReconcilerCorrectsObservedResourceDriftOnce` |
| `C18` | LIVE_ONLY | application | `test/pivot/commerce_v2_test.go::TestCommercial_CanceledBeforeExecutionCreatesNoUsageCharge` |
| `G1` | REUSED | domain/HTTP | `internal/agent/enrollment/enrollment_tdd_test.go::TestAgent_ProjectMCPTokenIsBoundToAgentUserTenantAndProject` |
| `G2` | REUSED | contract/golden | `test/contract/mcp_v2_test.go::TestMCPV2_CatalogAndSchemasAreVersionedStableAndClosed` |
| `G3` | REUSED | application | `test/contract/mcp_v2_test.go::TestMCPV1CompatibilityCannotBypassV2Governance` |
| `G4` | REUSED | fuzz/security | `internal/kernel/execution/execution_tdd_test.go::TestAgent_ToolArgumentsCannotOverrideVerifiedScope` |
| `G5` | REUSED | application/provider | `internal/workspace/service_tdd_test.go::TestWorkspace_CreateUsesPinnedImageDigestAndPolicyProfile` |
| `G6` | REUSED | provider/system | `internal/workspace/service_tdd_test.go::TestWorkspace_IsEphemeralAndDestroyRemovesDiskAndCredentials` |
| `G7` | REUSED | architecture/system | `internal/workspace/service_tdd_test.go::TestWorkspace_CommandRunsOnlyInsideWorkspaceNotControlPlaneHost` |
| `G8` | REUSED | policy | `internal/workspace/service_tdd_test.go::TestWorkspace_CommandPolicyRejectsForbiddenExecutableAndFlags` |
| `G9` | REUSED | system/network | `internal/workspace/service_tdd_test.go::TestWorkspace_NetworkProfileAllowsRequiredAndDeniesSensitiveDestinations` |
| `G10` | REUSED | integration/fuzz | `internal/security/redact/redact_tdd_test.go::TestWorkspace_StdoutStderrStreamingRedactsSecretsAcrossChunkBoundaries` |
| `G11` | REUSED | system | `internal/workspace/service_tdd_test.go::TestWorkspace_CommandTimeoutKillsProcessTreeAndMarksUsage` |
| `G12` | REUSED | chaos | `internal/workspace/service_tdd_test.go::TestWorkspace_RestartRecoversDurableCommandOutcomeWithoutRepeatingApply` |
| `G13` | REUSED | concurrency | `internal/workspace/service_tdd_test.go::TestWorkspace_ConcurrentCommandPolicySerializesStatefulOperations` |
| `G14` | REUSED | application | `internal/project/mcp/handler_test.go::TestAgent_DeployWorkflowStartsFromExactRepositoryRevision` |
| `G15` | REUSED | application/OpenTofu integration | `test/pivot/agent_workspace_test.go::TestInfraPlan_IsPureIdempotentAndStoresCanonicalPlanHash` |
| `G16` | REUSED | cross-domain | `test/pivot/agent_workspace_test.go::TestInfraApply_RequiresMatchingPlanReservationAndApproval` |
| `G17` | REUSED | live state backend/concurrency | `test/integration/postgres_remotestate_test.go::TestInfraState_RemoteLockPreventsConcurrentMutation` |
| `G18` | LIVE_ONLY | orchestration | `test/pivot/agent_workspace_test.go::TestAgent_GitOpsCommitOccursOnlyAfterInfraDependenciesReady` |
| `G19` | REUSED | acceptance | `test/acceptance/agent_lifecycle_acceptance_test.go::TestAgent_HealthFailureCreatesNewPatchCommitNotDirectClusterFix` |
| `G20` | REUSED | application | `test/pivot/agent_workspace_test.go::TestAgent_DestroyWorkflowShowsPlanAndRetainsResourcesByPolicy` |
| `G21` | LIVE_ONLY | security acceptance | `test/pivot/agent_workspace_test.go::TestAgent_NeverReceivesPlatformOrProviderMasterCredential` |
| `G22` | REUSED | cross-domain | `internal/project/mcp/handler_test.go::TestAgent_SecretSetIsWriteOnlyAcrossMCPAndWorkspace` |
| `G23` | LIVE_ONLY | application | `test/pivot/agent_workspace_test.go::TestAgent_CostThresholdPausesBeforeApplyAndNotifiesHuman` |
| `G24` | REUSED | PostgreSQL/concurrency | `test/integration/postgres_infrastructure_test.go::TestAgent_ApprovalIsSingleUseAndBoundToCanonicalPlan` |
| `G25` | REUSED | application | `internal/agent/application/agent_tdd_test.go::TestAgent_RepairLoopAndBudgetStopAutonomousSpend` |
| `G26` | LIVE_ONLY | gateway acceptance | `test/pivot/agent_workspace_test.go::TestAgent_ProviderCapabilityUsageVisibleWithoutMasterCredential` |
| `G27` | LIVE_ONLY | cross-domain | `test/pivot/agent_workspace_test.go::TestAgent_SuspendedTenantCanInspectButCannotMutate` |
| `G28` | REUSED | acceptance | `test/acceptance/agent_lifecycle_acceptance_test.go::TestAgent_AuditConnectsIntentTaskCommandsCommitsPlansApprovalsAndRuntime` |
| `G29` | LIVE_ONLY | chaos/provider | `test/pivot/agent_workspace_test.go::TestAgent_CancelDuringApplyReconcilesActualExternalState` |
| `G30` | REUSED | HTTP/security | `internal/identity/httpauth/middleware_test.go::TestAgent_APIRequiresOIDCOrMTLSAndRejectsIdentityHeadersInProduction` |
| `E2E-1` | LIVE_ONLY | full system | `test/system/agentic_devops_e2e_test.go::TestSystem_OneButtonDeploySimpleGoService` |
| `E2E-2` | LIVE_ONLY | full provider-backed system | `test/system/agentic_devops_e2e_test.go::TestSystem_DeployTemporalPostgresQdrantAndOpenRouter` |
| `E2E-3` | LIVE_ONLY | system | `test/system/agentic_devops_e2e_test.go::TestSystem_CustomUnknownServiceUsesGenericHelmPath` |
| `E2E-4` | LIVE_ONLY | security/system | `test/system/agentic_devops_e2e_test.go::TestSystem_DestructivePlanCannotReusePreviousApproval` |
| `E2E-5` | LIVE_ONLY | chaos | `test/system/agentic_devops_e2e_test.go::TestSystem_LostGitWebhookProviderResponseAndArgoStatusRecover` |
| `E2E-6` | LIVE_ONLY | offensive security | `test/system/agentic_devops_e2e_test.go::TestSystem_WorkspaceCompromiseCannotReachControlPlaneOrOtherTenant` |
| `E2E-7` | LIVE_ONLY | system/commerce | `test/system/agentic_devops_e2e_test.go::TestSystem_CostBudgetStopsAutonomousRepairLoop` |
| `E2E-8` | LIVE_ONLY | system | `test/system/agentic_devops_e2e_test.go::TestSystem_RuntimeDriftReconcilesFromGitNotWorkspaceMemory` |
| `E2E-9` | LIVE_ONLY | system/provider | `test/system/agentic_devops_e2e_test.go::TestSystem_ProjectDeletionRetainsAndPurgesAccordingToExplicitPolicy` |
| `E2E-10` | LIVE_ONLY | full security regression | `test/system/agentic_devops_e2e_test.go::TestSystem_SecretSentinelAbsentAcrossAllSurfaces` |
| `E2E-11` | LIVE_ONLY | system | `test/system/agentic_devops_e2e_test.go::TestSystem_SuspensionStopsMutationButPreservesRecoverability` |
| `E2E-12` | LIVE_ONLY | disaster recovery | `test/system/agentic_devops_e2e_test.go::TestSystem_BackupRestoreReconstructsControlAndProjectState` |
