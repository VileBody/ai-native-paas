# GitLab unknown-project quarantine — 2026-07-14

Status: `LOCAL_GREEN`, `DB_GREEN`; the live GitLab.com inventory gate remains
pending.

Implemented:

- Repository creation assigns both a high-entropy correlation marker and the
  `ai-native-paas` GitLab project topic.
- Provider discovery lists only direct projects in the configured group, with
  shared projects and subgroups disabled, stable numeric-ID ordering, 100-item
  pagination and a fixed page budget.
- Lost-response adoption requires both the exact correlation marker and the
  managed topic. A path match alone is never sufficient.
- Reconciliation skips already-bound numeric GitLab project IDs. An unknown
  project whose path collides with an expected platform repository becomes a
  provider quarantine observation; `repositories.provider_project_id` is not
  changed.
- Missing/mismatched external identity, missing managed topic and an otherwise
  valid but unbound managed project have separate bounded reasons.
- Quarantine is platform inventory, not a customer resource: its table has no
  `tenant_id` and no foreign key that could adopt the candidate repository.
  Customer tenant identity is also absent from the quarantine outbox payload.
- A repeated identical observation updates `last_observed_at` without emitting
  duplicate security events. Material changes produce new evidence.
- PostgreSQL migration `006_provider_project_quarantine.sql` persists the
  identity, reason, first/last observation and optimistic version.

The discovery contract follows GitLab's official
[Groups API](https://docs.gitlab.com/api/groups/#list-projects), including
pagination, `include_subgroups` and `with_shared`. Project identity labeling
uses the supported `topics` field from the official
[Projects API](https://docs.gitlab.com/api/projects/#create-a-project).

Executable evidence:

- `TestSource_UnknownProviderProjectIsQuarantinedNotAdopted`
- `TestGitLab_ListNamespaceRepositoriesReturnsManagedIdentityWithoutSharedProjects`
- `TestGitLab_CreateConflictRecoversByCorrelationMarker`
- `TestMigrations_ProviderQuarantineCannotBecomeTenantBinding`
- `TestPostgres_SourceMigrationsCleanInstallAndUpgrade`
- `TestPostgres_ProviderProjectQuarantineRoundTripsWithoutTenantBinding`
- `go test ./...`
- `go vet ./...`
- `go vet -tags=integration_postgres ./internal/source/postgres`
- scoped Source/pivot/architecture race tests
- pivot matrix regeneration and `--check`

Managed PostgreSQL evidence:

- A temporary admin-cluster client pod ran the clean-install/idempotent
  migration gate and real Store quarantine round-trip against the Timeweb
  managed test database.
- The live schema check confirmed that the quarantine table has no
  `tenant_id` column.
- Database credentials remained in the existing Kubernetes Secret and were
  not printed or persisted locally. The pod and local Linux test binary were
  deleted after the run.

Matrix effect:

- S18 is now `REUSED` executable reconciliation evidence.
- The complete matrix remains mapped at 157 requirements.
- Discovered Go test/fuzz targets: 907.
- Statuses: `REUSED 46`, `NEW 44`, `LIVE_ONLY 67`.

Not claimed:

- No unknown project was created in the live GitLab.com beta group.
- Real GitLab token isolation, rename/transfer and lifecycle mutation gates
  remain pending.
