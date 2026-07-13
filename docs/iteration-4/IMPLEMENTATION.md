# Iteration 4 — Runtime Delivery implementation

## Domain boundary

Runtime Delivery owns:

- `Application`;
- `Environment`;
- `RuntimeCell`;
- `Placement` and explicit placement migration;
- `Release`;
- `Deployment`;
- GitOps commit records;
- observed runtime status and quarantine records.

PostgreSQL schema: `runtime`.

It consumes the frozen Build-domain `ArtifactRef` and `ArtifactPolicy`. It does not read Build tables and does not ask GitLab for branch state.

## Source-of-truth split

```text
Runtime PostgreSQL   commercial/domain identity and operation history
GitOps repository    desired PaaSApp state
Kubernetes           observed runtime state
Artifact registry    immutable OCI content addressed by digest
```

No single field is accepted from one source when an independent authoritative source is available. Most importantly, Kubernetes status cannot activate a release unless the observed PaaSApp spec exactly matches the release persisted in runtime PostgreSQL.

## Release workflow

```text
DeployRequest
  ├─ tenant/application/environment validation
  ├─ ArtifactPolicy exact-digest validation
  ├─ deterministic release identity
  ├─ capacity computation from unit × minimum replicas
  ├─ sticky placement or compatible-cell selection
  ├─ release + deployment + outbox transaction
  ├─ deterministic PaaSApp render
  ├─ Git commit
  ├─ Git commit record transaction
  └─ deployment phase GIT_COMMITTED
```

Git push and PostgreSQL cannot share one transaction. The commit includes deterministic release metadata, and `FindByRelease` allows reconciliation after the failure window “push succeeded, DB update failed”.

## Placement

Placement is stable across normal releases. Selection filters on:

- region;
- isolation class;
- cell lifecycle/draining state;
- capacity.

Capacity reservation equals:

```text
unit weight × sum(process.minReplicas)
```

A configuration change that alters baseline capacity resizes the existing placement and cell allocation transactionally. Moving an environment to another cell is an explicit, audited placement migration.

## GitOps

The renderer emits only:

```text
namespace.yaml
paasapp.yaml
```

Path:

```text
cells/<cell>/tenants/<tenant>/apps/<application>/<environment>/
```

All path components are canonicalized and checked against traversal and symlink escape. Rendered output is deterministic and includes exact `repository@sha256:...`, never a mutable tag or secret value.

The local Git adapter used for integration tests performs real commits and recovers by release ID. Production remote push credentials are intentionally not embedded in this iteration.

## Controller ownership

```text
Argo CD owns
  Namespace
  PaaSApp

PaaS Operator owns
  Deployment
  Service
  HorizontalPodAutoscaler
  HTTPRoute
  migration Job
  NetworkPolicy
  PaaSApp.status
```

Argo does not own child replica counts and ignores `/status`. This prevents Argo/HPA/operator reconciliation loops.

## PaaSApp operator

The reconciler is implemented as a pure orchestration layer over a narrow Kubernetes client port. Both a deterministic in-memory API and a raw REST adapter implement that port.

For sandboxed workloads the generated pod specification includes:

- `runtimeClassName: gvisor`;
- `automountServiceAccountToken: false`;
- `runAsNonRoot: true`;
- `allowPrivilegeEscalation: false`;
- read-only root filesystem;
- dropped Linux capabilities;
- RuntimeDefault seccomp;
- requests, limits and ephemeral-storage limits.

### Rollout ordering

```text
1. Create/observe migration Job if configured.
2. Do not create candidate workloads until migration succeeds.
3. Create candidate Deployments, Services and optional HPA.
4. Keep route on the verified previous backend while candidate is unready.
5. Switch route only when every required process is ready.
6. Persist active backend identity in PaaSApp status.
7. On the next stable reconcile, clean superseded child resources.
```

A migration or readiness failure never switches traffic to the candidate. A successful migration is marked by release ID and is not repeated during retry.

### Lifecycle

Suspension removes traffic before compute. Resumption runs through the normal readiness gate. Deletion is two-phase and waits for observed child-resource disappearance before entering retention. Attachment references are never deleted by Runtime Delivery.

## Status trust boundary

Before accepting observed status, the control plane compares:

- API version and kind;
- object namespace and deterministic name;
- tenant, project, application and environment identity;
- release identity;
- image repository, digest and media type;
- every immutable `PaaSApp.spec` field reconstructed from runtime state;
- `status.observedGeneration >= metadata.generation`.

Mismatch returns a conflict and cannot activate the deployment. Unknown objects are quarantined.

A `Degraded` observation is recoverable. The release remains `DEPLOYING`; a later valid `Ready` observation for the same immutable candidate transitions it to `ACTIVE`.

## Persistence

Three migrations create and harden the runtime schema:

1. tables, indexes, idempotency, outbox and audit;
2. append-only and immutable-identity triggers;
3. JSON payload/authoritative-column consistency triggers.

The store uses serializable transactions, optimistic versions and domain-to-SQL mapping. There are no cross-domain foreign keys.

## Executables

`runtime-api` is a development integration binary using an in-memory store and local Git repository. Authentication is represented by development headers and is not production OIDC middleware.

`runtime-operator` is a polling in-cluster binary using service-account credentials and the raw Kubernetes REST adapter. It builds successfully; live-cluster execution is deferred to the Kubernetes acceptance gate.
