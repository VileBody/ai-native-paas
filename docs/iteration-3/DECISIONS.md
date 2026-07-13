# Iteration 3 architecture decisions

## ADR-3.1 — Build consumes exact `SourceRevision`

**Decision:** the Build domain accepts a versioned value containing repository identity, branch metadata, exact commit SHA, and source root. It never resolves a mutable branch itself.

**Reason:** this preserves reproducibility and keeps GitLab credentials/provider behavior inside Source Control.

## ADR-3.2 — OCI digest is the only artifact authority

**Decision:** tags are navigation aids only. Build, scan, signature, policy, and future release records bind to `sha256:<64 hex>`.

**Reason:** mutable tags make rollback, provenance, and promotion ambiguous.

## ADR-3.3 — Trust state is persisted and independently guarded

**Decision:** aggregate methods and PostgreSQL triggers both enforce SBOM/scan/signature ordering and immutability.

**Reason:** domain code alone is insufficient protection against adapter defects, manual SQL, or later regressions.

## ADR-3.4 — Buildpacks are primary; Dockerfile is a different isolation class

**Decision:** ordinary source builds use the buildpacks/kpack port. Dockerfile execution uses a disposable-VM port and a separate backend identifier.

**Reason:** arbitrary Dockerfiles have a much larger privilege and daemon attack surface.

## ADR-3.5 — Secrets are references, not build configuration values

**Decision:** sensitive-looking keys are rejected from ordinary build environment configuration. A dedicated provider returns phase-scoped build secrets; runtime secrets are inaccessible.

**Reason:** build identity, logs, cache, and OCI layers must not accidentally become secret stores.

## ADR-3.6 — The local OCI builder is a test adapter

**Decision:** deterministic local OCI output validates orchestration and trust semantics without pretending to be production kpack/Paketo.

**Reason:** TDD needs a real artifact and exact digest in the local environment, while cluster-specific behavior belongs to external adapter acceptance tests.

## ADR-3.7 — No automatic resume from an arbitrary partial build yet

**Decision:** `RunBuild` starts only from `QUEUED`; a partially persisted build returns a stable conflict until a dedicated reconciler is implemented.

**Reason:** blindly replaying side effects around registry publication, scan, and signing can create duplicate or contradictory trust records.

## ADR-3.8 — Iteration 2 defects remain Iteration 2 patches

**Decision:** Source Control fixes found while integrating `SourceRevision` are documented separately and do not alter the Build bounded context.

**Reason:** iteration ownership must stay legible and test failures must be repaired at their source.
