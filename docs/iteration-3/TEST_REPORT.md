# Iteration 3 test report

## Verdict

**PASS — Build & Artifact local release gate is green.**

All tests specified in the Iteration 3 TDD document exist and pass. Live PostgreSQL verification also closed the five deferred Iteration 1 database tests.

## Environment

```text
Go          1.23.2 linux/amd64
Git         2.47.3
GCC         14.2.0
PostgreSQL  18.4
OS/kernel   Linux 4.4.0 x86_64
```

PostgreSQL tests used a real server on a local TCP endpoint, not an in-memory SQL fake.

## Inventory

```text
313 top-level Test... functions in cumulative repository
120 build-related top-level tests
 62/62 exact test names required by docs/tdd/03-build-and-artifact.md
  3 fuzz targets
  0 missing Iteration 3 TDD tests
```

## Default and static gates

Passed:

- `gofmt` clean;
- default `go vet ./...`;
- tagged vet for kernel/build PostgreSQL and Source PostgreSQL;
- `go test -count=1 ./...` across all packages;
- architecture boundaries;
- migration copy parity;
- no cross-domain internal imports;
- no cross-schema foreign keys;
- no destructive Build migration statements;
- no direct wall-clock access in Build domain/application;
- production-file scan for fixture secret literals;
- all three binaries compile.

## Race detector

The complete package set was divided into two independent invocations because the execution host terminated one long chained command after the first group. Both groups independently passed:

```text
Build + Artifact + acceptance + architecture  PASS
Kernel + Source + contracts                    PASS
```

No race was detected.

## Repetition and order sensitivity

```text
Fast Build/domain/adapter packages: shuffle enabled × 20  PASS
Real Git/OCI/source/acceptance fixtures: shuffle × 3      PASS
```

The real-fixture group repeatedly compiled/checksummed source trees, produced OCI layouts, exercised exact Git revisions, and completed the source-to-artifact acceptance flow.

## Fuzzing

```text
Kernel operation transition fuzz         521,528 executions  PASS
Source workspace path fuzz                 4,195 executions  PASS
Build config/path normalization fuzz     574,787 executions  PASS
Total                                   1,100,510 executions
```

The Build fuzz corpus expanded to 119 interesting inputs during the recorded run.

## Live PostgreSQL

### Iteration 3 Build schema

Eight live tests passed under the race detector:

```text
TestPostgres_BuildMigrationsCleanInstallAndUpgrade
TestPostgres_BuildAndOutboxAreAtomic
TestPostgres_ConcurrentBuildIdentityUsesSingleWinner
TestPostgres_BuildOptimisticLockPreventsLostUpdate
TestPostgres_ArtifactIdentityIsImmutable
TestPostgres_BuildAuditRejectsMutation
TestPostgres_BuildPipelinePersistsReleasableTrustChain
TestPostgres_DockerfileExecutionSelectionAndIdentityRemainImmutable
```

They prove clean migration install/upgrade, atomic build/outbox persistence, concurrent single-winner identity, optimistic locking, immutable artifact identity, append-only audit, complete persisted trust chain, and immutable Dockerfile execution selection/build correlation.

### Iteration 1 deferred PostgreSQL gate

All five previously deferred kernel tests passed under the race detector:

```text
TestPostgres_Migrations_CleanInstallAndUpgrade
TestPostgres_CreateOrganizationAndOutboxAreAtomic
TestPostgres_ConcurrentIdempotencyUsesSingleWinner
TestPostgres_OperationOptimisticLockPreventsLostUpdate
TestPostgres_AuditTableRejectsUpdateAndDelete
```

Iteration 1 is therefore no longer conditional on PostgreSQL behavior in this environment.

### Iteration 2 compatibility patch

The Source tagged suite also passed against PostgreSQL 18.4. It covered 4 migration/static checks and 10 live persistence/concurrency checks, including webhook deduplication, out-of-order events, provider-identity immutability, append-only audit, and bounded Serializable retry.

## Acceptance coverage

The main acceptance scenario performs:

```text
exact Go commit
→ RequestBuild twice with one idempotency key
→ one build identity
→ exact source checkout
→ runtime detection
→ deterministic OCI output
→ tenant registry publish
→ immutable image digest
→ SBOM attachment
→ policy scan
→ Ed25519 signature + verification
→ persisted RELEASABLE artifact
→ ArtifactPolicy allow decision
```

Assertions verify that runtime and build fixture secrets are absent from build logs and the registry tree.

A monorepo scenario proves that canonical `source_root` limits build scope: the repository root is intentionally invalid while the selected Go component succeeds.

## Coverage

Package-local atomic statement coverage:

```text
Cumulative repository total            57.0%
Build application                      75.1%
Build domain                           80.0%
Build cache                            89.6%
Build logs                             88.5%
Build detector                         66.2%
Build HTTP API                         67.3%
Build kpack boundary                   71.9%
Local OCI adapter                      73.0%
Registry adapter                       75.8%
SBOM                                   75.0%
Scanner                                79.4%
Signer                                 72.4%
Source fetcher                         74.2%
Build v1 contract                      88.9%
```

The untagged coverage profile reports `0.0%` for PostgreSQL adapter packages because their live behavior is intentionally exercised in separately tagged race suites. Their pass/fail evidence is in `VERIFICATION.log`, not in the default coverage percentage.

## Binary smoke

Compiled:

```text
bin/kernel-api
bin/source-api
bin/build-api
```

A real `build-api` process was started on an ephemeral TCP port:

```http
GET /healthz
→ 200
→ {"status":"ok"}
```

## Verification execution note

The repository includes `scripts/verify-iteration-3.sh` as the single release-gate entrypoint. In this hosted execution environment, one long invocation was externally terminated during the race stage before its shell trap could record a verdict. Every stage was therefore rerun as an independent command and assembled into `docs/iteration-3/VERIFICATION.log`.

The `PASS` verdict is based on those complete per-stage logs; the interrupted partial log is retained under `.verification/iteration-3/unified-script-host-interruption.log` for transparency.

## What was not tested locally

No claim is made for live kpack, Harbor, Trivy, cosign/KMS, KubeVirt disposable VMs, production sandbox enforcement, multi-architecture images, cluster egress policy, registry replication, or chaos/failover. These are itemized in `TODO.md` and remain required before public untrusted builds.
