# Project v2 repository bootstrap — 2026-07-14

Status: `LOCAL_GREEN`, `DB_GREEN`; the live GitLab.com bootstrap gate remains
pending.

Implemented:

- Project creation provisions a private GitLab repository in the configured
  platform namespace and protects its default branch before bootstrap.
- The canonical bootstrap is one exact-base batch commit containing
  `platform.yaml`, `.gitignore`, `README.md`, `infrastructure/tofu`,
  `deploy/base`, development and production GitOps environments, and
  `recipes.lock.yaml`.
- `platform.yaml` uses the strict v2 parser contract and pins the workspace
  image by immutable digest. Production and destructive paths require
  approval by default.
- The commit is bound to the provider branch head observed before mutation and
  carries the repository correlation trailer.
- The returned 40- or 64-character commit SHA is normalized and persisted as
  the repository's immutable `bootstrap_revision`. A different revision
  cannot overwrite it.
- The same transaction advances the authoritative default-branch head and
  emits `source.revision_observed.v2` with bounded reason `bootstrap`, a
  repository-bootstrap outbox event and tenant-scoped audit evidence.
- Agent enrollment is issued only after bootstrap revision persistence has
  succeeded.
- PostgreSQL migration `005_bootstrap_revision.sql` adds the durable column and
  database-level SHA-shape constraint plus a write-once trigger. The memory
  adapter enforces the same immutability boundary.

Executable evidence:

- `TestSource_CreateProjectBootstrapsV2RepositoryLayout`
- `TestCreateProject_ProvisionsBootstrapsAndIssuesBoundEnrollment`
- `TestBootstrapFiles_ContainStrictPlatformContractAndCanonicalRoots`
- `TestRepository_BootstrapRevisionIsImmutable`
- `TestGitLab_BootstrapRepositoryUsesExactBaseAndBatchCommit`
- `TestGitLab_BootstrapLostResponseDiscoversExactFiles`
- `TestPostgres_SourceMigrationsCleanInstallAndUpgrade`
- `TestPostgres_BootstrapRevisionRoundTripsThroughStore`
- `go test ./...`
- `go vet ./...`
- scoped race tests for source, project, contracts, pivot and architecture
- pivot matrix regeneration and `--check`

Managed PostgreSQL evidence:

- A Linux test binary and all five Source migrations were copied into a
  temporary client pod in the admin cluster. Database credentials remained in
  the existing Kubernetes Secret and were not printed or persisted locally.
- Clean install, migration idempotency and the real Store bootstrap-revision
  round-trip passed against the Timeweb managed test database. A direct SQL
  attempt to replace the stored SHA was rejected and the original value
  remained intact.
- The temporary pod and local binary were deleted after the run. The final
  Source test schema was reconstructed from all five migrations.

Matrix effect:

- S1 is now `REUSED` executable GitLab-contract evidence.
- The complete matrix remains mapped at 157 requirements.
- Discovered Go test/fuzz targets: 903.
- Statuses: `REUSED 45`, `NEW 44`, `LIVE_ONLY 68`.

Not claimed:

- No repository was created or bootstrapped in the live GitLab.com beta group.
- Real GitLab token isolation, rename/transfer, lifecycle mutation and unknown
  project quarantine gates remain pending.
