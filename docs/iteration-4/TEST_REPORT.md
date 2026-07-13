# Iteration 4 — Test report

Test date: **2026-07-12**  
Go toolchain: **Go 1.23.2**  
Live database: **PostgreSQL 18.4**

## Test inventory

```text
Cumulative unique Test functions       419
Cumulative Test function occurrences   420
Iteration-4-related unique tests        103
Required TDD tests                    63/63
Runtime live PostgreSQL tests           9/9
Iteration 4 fuzz targets                3/3
```

The exact TDD-to-file mapping is in `TDD_MATRIX.md`. Machine-readable inventory and parity evidence are under `evidence/`.

## Default regression

```bash
gofmt check
go vet ./...
CGO_ENABLED=1 go vet -tags=postgres_integration ./test/integration
go vet -tags=integration_postgres ./internal/source/postgres
go test ./... -count=1
```

Result: **PASS**.

This includes all earlier domains plus runtime domain, application orchestration, GitOps, HTTP, raw Kubernetes REST adapter, operator, contracts, architecture tests and acceptance tests.

## Race detector

Authoritative Iteration 4 race gate:

```bash
go test -race -count=1 \
  ./internal/runtime/... \
  ./pkg/contracts/runtime/v1 \
  ./test/contract \
  ./test/architecture

go test -race -count=1 ./test/acceptance -run '^TestRuntimeDelivery_'
```

Result: **PASS**.

A cumulative `go test -race ./...` did not complete within the environment's 20-minute command limit. It emitted no race failure before termination and is recorded as **INCOMPLETE** rather than PASS.

## Live PostgreSQL

Runtime tests executed against PostgreSQL 18.4 with the race detector:

```text
TestPostgres_RuntimeMigrationsCleanInstallAndUpgrade
TestPostgres_RuntimeApplicationReleaseDeploymentAndOutboxAreAtomic
TestPostgres_RuntimeConcurrentReleaseIdentityUsesSingleWinner
TestPostgres_RuntimeOptimisticLockPreventsLostUpdate
TestPostgres_RuntimeReleaseArtifactAndConfigAreImmutable
TestPostgres_RuntimeGitOpsCommitIsImmutable
TestPostgres_RuntimeAuditRejectsMutation
TestPostgres_RuntimePayloadConsistencyRejectsColumnDrift
TestPostgres_RuntimeOnlyOneActiveReleasePerEnvironment
```

Result: **9 passed, 0 failed, 0 skipped**.

The full cumulative PostgreSQL suite for Iterations 1–4 also passed under `-race`.

The database gates prove:

- clean install and idempotent migration upgrade;
- atomic application/release/deployment/outbox persistence;
- single-winner concurrent release identity;
- optimistic-lock protection;
- immutable artifact, digest, configuration and rollback identity;
- append-only GitOps commit and audit records;
- JSON payload/authoritative-column consistency;
- at most one active release per environment.

## Fuzzing

```text
FuzzPaaSAppValidateNeverPanics              161,658 executions
FuzzGitOpsPathValidationCannotEscape        528,956 executions
FuzzRuntimeObjectNamesRemainDNSBounded      123,789 executions
                                               ─────────
Confirmed completed executions              814,403
```

Result: **PASS**.

The first grouped naming run was interrupted by the aggregate tool limit after three seconds; the same target was immediately rerun separately to completion. Only completed runs are counted above.

## Shuffle and repetition

```text
Runtime fast packages / contracts / architecture   shuffle ×25  PASS
Real local-Git GitOps suite                        shuffle ×5   PASS
Runtime acceptance                                 shuffle ×10  PASS
```

These runs target hidden test-order coupling, shared mutable fixtures and idempotency assumptions.

## Acceptance without Kubernetes

`TestRuntimeDelivery_CommitToReadyAndRollbackWithoutLiveKubernetes` composes the real runtime service, deterministic GitOps renderer, GitOps commit port, PaaS operator and in-memory Kubernetes API model.

It proves:

1. artifact A creates a GitOps-committed deployment;
2. the operator creates a candidate workload;
3. readiness activates release A;
4. artifact B rolls out and supersedes A;
5. rollback creates a new auditable release using A's exact digest;
6. rollback makes zero Build-domain calls.

Result: **PASS**.

## Runtime API process smoke

A real `runtime-api` process was started on an ephemeral port. The smoke script proved:

```text
health                  PASS
tenant derived from auth boundary  PASS
GitOps committed deployment       PASS
status readable                   PASS
cross-tenant request denied       PASS
```

## Static delivery artifacts

PyYAML successfully parsed:

- one ApplicationSet;
- one AppProject;
- one PaaSApp CRD;
- one operator Deployment;
- ServiceAccount, ClusterRole and ClusterRoleBinding.

Contract tests additionally prove:

- Argo manages only Namespace and `PaaSApp`, not generated children;
- the default Argo project is not used;
- auto-sync, prune and self-heal are configured;
- source and destination are restricted;
- removal of ApplicationSet preserves customer resources;
- CRD is namespaced and has a status subresource;
- operator RBAC has no Secret or Namespace mutation privileges.

## Coverage

```text
Untagged runtime statement coverage                 61.9%
PostgreSQL-tagged runtime/application/domain total  32.9%
```

The two profiles are separate because the live PostgreSQL adapter is behind a build tag and is exercised only in the database suite. HTML reports are included at repository root.

## Evidence

Raw outputs are in `docs/iteration-4/evidence/`. The files are retained intentionally so the final status can be audited without trusting this summary.
