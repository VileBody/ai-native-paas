# Iteration 4 — Final status

## Verdict

```text
ITERATION_4 = GREEN
SCOPE       = WITHOUT LIVE KUBERNETES
POSTGRESQL  = GREEN (PostgreSQL 18.4, race detector)
KUBERNETES  = NOT RUN / EXPLICITLY DEFERRED
```

Iteration 4 is complete for every behavior that can be tested faithfully in the current environment without a Kubernetes API server. The remaining work is not hidden inside a generic TODO: all live-cluster assertions are isolated in `KUBERNETES_TODO.md` and must become a separate infrastructure acceptance gate.

## Delivered path

```text
immutable ArtifactRef
        ↓ ArtifactPolicy
Release + sticky Placement
        ↓ deterministic renderer
GitOps commit
        ↓ Argo contract
PaaSApp v1alpha1
        ↓ PaaS Operator
migration → candidate workloads → readiness → route switch
        ↓ observed status
Deployment READY
        ↓
rollback creates a new auditable release using the previous digest
```

## Completed gates

- all **63/63** exact tests from the Iteration 4 TDD specification exist;
- **103** unique Iteration-4-related tests are present in the repository;
- the complete cumulative untagged suite passes;
- targeted Iteration 4 race suites pass;
- nine live runtime PostgreSQL tests pass under `-race`;
- all cumulative PostgreSQL tests from Iterations 1–4 pass under `-race`;
- three Iteration 4 fuzz targets completed **814,403 confirmed executions** without panic or invariant violation;
- deterministic local-Git GitOps tests pass under shuffled repetition;
- the no-Kubernetes end-to-end acceptance path passes;
- runtime API process smoke passes, including cross-tenant denial;
- CRD, RBAC, ApplicationSet and AppProject YAML parse and contract tests pass;
- runtime API and operator binaries build;
- production-scope secret-literal and mutable-image scans pass;
- untagged runtime coverage is **61.9%**; PostgreSQL-tagged coverage is recorded separately.

## Important qualification

A complete cumulative `go test -race ./...` was attempted. It exceeded the 20-minute execution limit while running heavy Git/OCI suites inherited from previous iterations. No race failure was emitted before termination, but the command is recorded as **INCOMPLETE**, not PASS. The authoritative Iteration 4 race gate is the narrower suite covering every runtime package, runtime contract, architecture contract and runtime acceptance scenario; that gate passed.

## Defects found by the final gates

Live PostgreSQL found a real mismatch between the JSON form of `ArtifactRef` and migration `003_payload_consistency`: the trigger used nested CamelCase paths although `ArtifactRef` uses snake_case JSON tags. Both the root and embedded migrations were corrected, and the full live PostgreSQL suite then passed.

The local PostgreSQL harness also had a lifecycle defect: `exec "$@"` bypassed the shell EXIT trap and could leave the postmaster alive after a failed test. It now invokes the command normally, guaranteeing cleanup.

Status reconciliation was hardened before final verification. A `Ready` status is rejected unless metadata, tenant, project, application, environment, release, exact image, immutable spec and `observedGeneration` all match authoritative runtime state. A transient `Degraded` candidate may recover to `Ready` without creating a new release.

## Exit decision

Iteration 5 may consume the frozen runtime contracts. Kubernetes-specific failures discovered later must be fixed as Iteration 4 follow-up work; they must not be bypassed in Attachments or later domains.
