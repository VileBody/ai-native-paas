# Source reconciliation and preview cleanup — 2026-07-14

Status: `LOCAL_GREEN`, `DB_GREEN`; GitLab.com provider-live gates remain pending.

Implemented:

- Added canonical `source.revision_observed.v2` and
  `source.environment_cleanup_requested.v2` contracts with bounded, validated
  payloads.
- Webhook receipts now retain delivery ordering. A delivery older than the
  accepted branch event is committed as `source.push.stale` but cannot update
  the observed head or publish another revision event.
- Non-deletion webhooks still publish the legacy `source.push.v1` event as an
  additive compatibility path. The canonical v2 revision event is published
  only when the authoritative head actually changes.
- The periodic reconciler compares the stored default-branch head with the
  provider head and publishes exactly one v2 revision event for a missed
  change. A second reconciliation is a no-op.
- Preview branch to environment identity is durable, tenant-scoped,
  idempotent, and unique per repository. The default branch cannot be bound as
  a preview environment.
- A signed GitLab branch-deletion webhook persists deletion metadata and emits
  one cleanup intent for the exact mapped environment. Source has no runtime
  deletion dependency and retains the last immutable revision for audit and
  recovery.
- PostgreSQL migration `004_branch_environment_cleanup.sql` adds durable
  `environment_id` and `deleted_at` state plus the unique identity index.

Executable evidence:

- `TestSource_MissedWebhookRecoveredByBranchReconciler`
- `TestSource_OutOfOrderWebhookCannotRegressObservedHead`
- `TestSource_DeletedBranchProducesEnvironmentCleanupIntent`
- `TestPreviewEnvironmentBindingIsTenantScopedAndUnique`
- `TestPostgres_BranchEnvironmentBindingAndDeletionPersist`
- `TestPostgres_BranchEnvironmentStateRoundTripsThroughStore`
- `go test ./...`
- `go vet ./...`
- `go vet -tags=integration_postgres ./internal/source/postgres`
- `go test -race ./...`
- pivot matrix regeneration and `--check`

Managed PostgreSQL evidence:

- The full tagged Source PostgreSQL suite passed against the Timeweb managed
  test database from a temporary in-cluster PostgreSQL client pod.
- A second focused run passed the real `postgres.Store` round-trip for mapped
  environment and deletion metadata.
- Migration files and the Linux test binary were copied into the temporary
  pod; no database credential was printed or persisted in the repository.
- Both temporary pods were deleted with a wait after their runs. The final
  managed test schema was reconstructed from all four Source migrations.

Matrix effect:

- S9, S10 and S13 are now `REUSED` executable evidence.
- The complete matrix remains mapped at 157 requirements.
- Discovered Go test/fuzz targets: 890.
- Statuses: `REUSED 41`, `NEW 44`, `LIVE_ONLY 72`.

Not claimed:

- This does not make the GitLab.com live provider gate green.
- Runtime consumption of the cleanup intent and real Argo preview teardown
  remain part of the later GitOps/runtime slice.
