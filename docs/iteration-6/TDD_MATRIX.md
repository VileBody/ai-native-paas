# Iteration 6 — TDD matrix

Обязательных сценариев: **52**.

| # | Test | Implementation | Status |
|---:|---|---|---|
| 1 | `TestPlan_ActiveVersionIsImmutable` | `internal/commerce/application/commercial_tdd_test.go:105` | PASS |
| 2 | `TestPlan_NewVersionDoesNotChangeExistingPeriod` | `internal/commerce/application/commercial_tdd_test.go:112` | PASS |
| 3 | `TestPlan_DisabledVersionRejectsNewSubscription` | `internal/commerce/application/commercial_tdd_test.go:131` | PASS |
| 4 | `TestPlan_MeterReferencesMustExist` | `internal/commerce/application/commercial_tdd_test.go:144` | PASS |
| 5 | `TestPlan_PricesUseIntegerMinorUnits` | `internal/commerce/application/commercial_tdd_test.go:151` | PASS |
| 6 | `TestEntitlement_AllowsFeatureIncludedInPlan` | `internal/commerce/application/commercial_tdd_test.go:161` | PASS |
| 7 | `TestEntitlement_DeniesMissingFeature` | `internal/commerce/application/commercial_tdd_test.go:168` | PASS |
| 8 | `TestEntitlement_DeniesSuspendedAccount` | `internal/commerce/application/commercial_tdd_test.go:175` | PASS |
| 9 | `TestEntitlement_TrialHasExplicitLimits` | `internal/commerce/application/commercial_tdd_test.go:185` | PASS |
| 10 | `TestEntitlement_DecisionContainsReasonAndPolicyVersion` | `internal/commerce/application/commercial_tdd_test.go:192` | PASS |
| 11 | `TestEntitlement_CrossTenantLookupDenied` | `internal/commerce/application/commercial_tdd_test.go:199` | PASS |
| 12 | `TestQuota_ReserveWithinLimit` | `internal/commerce/application/commercial_tdd_test.go:205` | PASS |
| 13 | `TestQuota_RejectAboveLimit` | `internal/commerce/application/commercial_tdd_test.go:212` | PASS |
| 14 | `TestQuota_ConcurrentReservationsCannotOversubscribe` | `internal/commerce/application/commercial_tdd_test.go:217` | PASS |
| 15 | `TestQuota_ReleaseMakesCapacityAvailable` | `internal/commerce/application/commercial_tdd_test.go:245` | PASS |
| 16 | `TestQuota_CommitIsIdempotent` | `internal/commerce/application/commercial_tdd_test.go:258` | PASS |
| 17 | `TestQuota_ExpiredReservationIsReclaimed` | `internal/commerce/application/commercial_tdd_test.go:271` | PASS |
| 18 | `TestUsage_AppendValidEvent` | `internal/commerce/application/commercial_tdd_test.go:284` | PASS |
| 19 | `TestUsage_DuplicateIdempotencyKeyIgnored` | `internal/commerce/application/commercial_tdd_test.go:291` | PASS |
| 20 | `TestUsage_SameKeyDifferentQuantityRejected` | `internal/commerce/application/commercial_tdd_test.go:299` | PASS |
| 21 | `TestUsage_NegativeQuantityRejectedExceptCorrectionType` | `internal/commerce/application/commercial_tdd_test.go:305` | PASS |
| 22 | `TestUsage_EventOutsideTenantResourceRejected` | `internal/commerce/application/commercial_tdd_test.go:311` | PASS |
| 23 | `TestUsage_LateEventRoutesToCorrectOpenPeriod` | `internal/commerce/application/commercial_tdd_test.go:316` | PASS |
| 24 | `TestRuntimeUsage_OneUnitForOneHourEquals3600UnitSeconds` | `internal/commerce/application/commercial_tdd_test.go:329` | PASS |
| 25 | `TestRuntimeUsage_ReplicaScaleProducesPiecewiseUsage` | `internal/commerce/application/commercial_tdd_test.go:335` | PASS |
| 26 | `TestRuntimeUsage_SuspensionStopsComputeUsageAtBoundary` | `internal/commerce/application/commercial_tdd_test.go:341` | PASS |
| 27 | `TestRuntimeUsage_ClockSkewDoesNotCreateNegativeInterval` | `internal/commerce/application/commercial_tdd_test.go:347` | PASS |
| 28 | `TestRuntimeUsage_OverlappingObservationsAreDeduplicated` | `internal/commerce/application/commercial_tdd_test.go:353` | PASS |
| 29 | `TestBuildRating_SuccessIsCharged` | `internal/commerce/application/commercial_tdd_test.go:360` | PASS |
| 30 | `TestBuildRating_UserFailureIsChargedByPolicy` | `internal/commerce/application/commercial_tdd_test.go:370` | PASS |
| 31 | `TestBuildRating_PlatformFailureIsNotCharged` | `internal/commerce/application/commercial_tdd_test.go:379` | PASS |
| 32 | `TestBuildRating_CanceledBeforeStartIsNotCharged` | `internal/commerce/application/commercial_tdd_test.go:388` | PASS |
| 33 | `TestBuildRating_DockerVMUsesSeparateMeter` | `internal/commerce/application/commercial_tdd_test.go:399` | PASS |
| 34 | `TestRating_UsesUTCPeriodBoundaries` | `internal/commerce/application/commercial_tdd_test.go:412` | PASS |
| 35 | `TestRating_DaylightSavingHasNoEffect` | `internal/commerce/application/commercial_tdd_test.go:421` | PASS |
| 36 | `TestRating_RoundsOnlyAtDefinedStage` | `internal/commerce/application/commercial_tdd_test.go:433` | PASS |
| 37 | `TestRating_MicroUsageAccumulatesBeforeRounding` | `internal/commerce/application/commercial_tdd_test.go:443` | PASS |
| 38 | `TestRating_LargeQuantityDoesNotOverflow` | `internal/commerce/application/commercial_tdd_test.go:469` | PASS |
| 39 | `TestInvoicePreview_IsDeterministic` | `internal/commerce/application/commercial_tdd_test.go:476` | PASS |
| 40 | `TestInvoicePreview_GroupsByMeterAndResource` | `internal/commerce/application/commercial_tdd_test.go:493` | PASS |
| 41 | `TestInvoicePreview_AppliesIncludedAllowanceBeforeOverage` | `internal/commerce/application/commercial_tdd_test.go:505` | PASS |
| 42 | `TestInvoicePreview_CreditsAreSeparateLedgerEntries` | `internal/commerce/application/commercial_tdd_test.go:546` | PASS |
| 43 | `TestInvoicePreview_PriceVersionMatchesUsageTimestamp` | `internal/commerce/application/commercial_tdd_test.go:558` | PASS |
| 44 | `TestCommercialState_GracePeriodDoesNotDeleteRuntime` | `internal/commerce/application/commercial_tdd_test.go:570` | PASS |
| 45 | `TestCommercialState_SuspensionEmitsSuspendIntent` | `internal/commerce/application/commercial_tdd_test.go:581` | PASS |
| 46 | `TestCommercialState_SuspensionRetainsManagedServices` | `internal/commerce/application/commercial_tdd_test.go:591` | PASS |
| 47 | `TestCommercialState_ResumptionEmitsResumeIntent` | `internal/commerce/application/commercial_tdd_test.go:598` | PASS |
| 48 | `TestCommercialState_AlreadySuspendedIsIdempotent` | `internal/commerce/application/commercial_tdd_test.go:606` | PASS |
| 49 | `TestUsageReconciler_ObservedAllocationMatchesLedger` | `internal/commerce/application/commercial_tdd_test.go:622` | PASS |
| 50 | `TestUsageReconciler_MissingIntervalCreatesCorrection` | `internal/commerce/application/commercial_tdd_test.go:630` | PASS |
| 51 | `TestUsageReconciler_DuplicateCorrectionIsPrevented` | `internal/commerce/application/commercial_tdd_test.go:641` | PASS |
| 52 | `TestUsageReconciler_DriftAboveThresholdRaisesAlert` | `internal/commerce/application/commercial_tdd_test.go:653` | PASS |
