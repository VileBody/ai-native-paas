# Iteration 4 — Kubernetes-only acceptance TODO

Current status: **NOT RUN**. The present environment had no Kubernetes API server, `kind`, `kubectl` or container runtime. The code does not claim these checks passed.

## 1. CRD/API-server behavior

- Install the CRD into a supported Kubernetes version.
- Prove structural schema acceptance, pruning and rejection of unknown fields.
- Prove CEL validations against invalid route/process and autoscaling combinations.
- Prove status subresource permissions and generation behavior.
- Test CRD upgrade compatibility for future additive versions.

## 2. Server-side apply and field ownership

- Run the raw REST adapter against a real API server.
- Inspect `managedFields` for Argo and operator ownership.
- Force field conflicts and prove the operator does not steal Argo-owned spec fields.
- Prove Argo ignores `/status` and never rewrites generated children/HPA replica state.
- Test resourceVersion conflicts, HTTP 409/429 handling and retry/backoff.

## 3. Argo CD

- Install the exact target Argo CD/ApplicationSet version.
- Validate the `files` generator against rendered repository layout.
- Prove restricted AppProject enforcement.
- Prove auto-sync, prune and self-heal behavior.
- Prove `preserveResourcesOnDeletion` during accidental ApplicationSet removal.
- Test sync-wave/ordering behavior for Namespace and PaaSApp.
- Test repository outage, webhook loss and polling recovery.

## 4. Operator controller lifecycle

- Run the operator with leader election and at least two replicas.
- Kill the active leader during rollout and migration.
- Test watch disconnect, relist/resync and API-server restart.
- Test namespace terminating/finalizer behavior.
- Test partial child deletion and reconciliation after operator restart.
- Test a high cardinality of PaaSApps for queue fairness and backpressure.

## 5. Workload isolation

- Install the Talos/containerd gVisor extension and `RuntimeClass` named `gvisor`.
- Prove sandboxed pods actually run under `runsc`, not runc fallback.
- Prove scheduling failure is surfaced when RuntimeClass is unavailable.
- Enforce Pod Security Admission `restricted` in app namespaces.
- Add Kyverno/Gatekeeper policy requiring gVisor, exact digest, limits, no privilege and no service-account token.
- Attempt privileged, hostPath, hostNetwork, hostPID, capability and device escapes.

## 6. Networking and routing

- Install the chosen Gateway API controller and actual Gateway/parentRefs.
- Prove HTTPRoute acceptance/status and default generated-host routing.
- Prove no traffic reaches a candidate before migration/readiness.
- Prove failed candidate keeps traffic on the verified previous Service.
- Prove route removal precedes workload suspension/deletion.
- Install Cilium policies and prove default deny, DNS allowance, explicit ingress and service-binding access.
- Prove egress gateway blocks RFC1918/control-plane/metadata/SMTP targets as designed. The current generic NetworkPolicy model does not establish those guarantees.

## 7. Autoscaling and resources

- Run real HPA controller tests and inspect replica ownership.
- Prove manual scale changes are reconciled through policy, not child mutation.
- Test CPU/memory/ephemeral-storage pressure, eviction and OOM behavior.
- Test PodDisruptionBudget and node-drain semantics when added.

## 8. Supply chain and registry

- Install image-signature admission policy.
- Prove unsigned, wrong-tenant and mutable-tag images are rejected.
- Test registry outage during reschedule/autoscaling.
- Test digest availability across target runtime cells.

## 9. Migration and rollout failure injection

- Execute a real successful migration Job exactly once.
- Inject migration failure, timeout and node loss.
- Inject failed readiness/startup probes and progress deadline timeout.
- Recover `Degraded → Ready` for the same immutable release.
- Verify superseded resources are deleted only after stable traffic switch.

## Required live acceptance scenario

```text
releasable digest
  → GitOps push
  → Argo applies PaaSApp
  → operator creates gVisor workload
  → migration succeeds
  → readiness succeeds
  → HTTP request reaches release A
  → release B fails readiness
  → HTTP request still reaches A
  → B recovers or rollback creates release C using A digest
```

Iteration 4 may be called **KUBERNETES GREEN** only after this document is converted into executable `envtest`/`kind`/real-cell suites and all mandatory checks pass.
