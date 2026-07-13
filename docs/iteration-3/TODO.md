# Iteration 3 explicit TODO

## Live infrastructure adapters

- [ ] Run the kpack adapter against the exact Kubernetes/kpack/Paketo versions selected for production.
- [ ] Verify lifecycle status mapping, cancellation ownership, timeout behavior, cache restoration, and exact digest collection against live kpack.
- [ ] Replace the local registry adapter with Harbor API/OCI Distribution integration and test auth, project quotas, attachment/referrer support, replication, garbage collection, and failover.
- [ ] Integrate the selected vulnerability scanner and freeze database-update/policy-version semantics.
- [ ] Replace the in-process Ed25519 signer with cosign plus KMS/HSM/OpenBao-backed key custody, rotation, revocation, and issuer policy.
- [ ] Implement the real KubeVirt disposable-VM Dockerfile backend and prove teardown under worker crash and control-plane restart.

## Build isolation and abuse controls

- [ ] Dedicated build cluster/node pools with no runtime/control-plane credentials.
- [ ] Enforce CPU, memory, PID, inode, file-size, wall-clock, and concurrent-build quotas.
- [ ] Enforce egress allowlists/proxies and deny metadata, private networks, SMTP, Kubernetes API, and control-plane networks.
- [ ] Add seccomp/AppArmor plus gVisor/Kata/VM boundary tests according to backend.
- [ ] Add malware, secret, decompression-bomb, path-count, and dependency-confusion tests.
- [ ] Add repository, Git LFS, layer, artifact, log, and cache size limits to commercial entitlements.

## Reconciliation and resilience

- [ ] Implement a durable build worker/reconciler that resumes or compensates partial stages using persisted side-effect receipts.
- [ ] Define exactly-once-equivalent behavior for ambiguous registry publish, attachment, scan, and sign timeouts.
- [ ] Add lease/heartbeat semantics for workers and deterministic timeout enforcement.
- [ ] Soak build storms, duplicate requests, cancellation races, and AI repair loops.
- [ ] Inject registry outage, scanner outage, signer outage, PostgreSQL failover, serialization conflicts, process kill, and disk exhaustion.
- [ ] Prove backup/restore of schema `build`, artifact metadata, signing metadata, and outbox state.

## Product wiring

- [ ] Wire `cmd/build-api` to PostgreSQL, authentication/authorization, durable operations, rate limiting, kpack, Harbor, scanner, signer, secret broker, and persistent logs.
- [ ] Add production HTTP server timeouts, graceful shutdown, request IDs, metrics, tracing, and audit actor context.
- [ ] Add an event publisher for outbox records and consumer contract tests.
- [ ] Add log retention, tenant query authorization, streaming/backpressure, and deletion policy.
- [ ] Add dependency proxies and tenant-safe cache policies for npm, pip, Go, Maven, and OCI bases.

## Reproducibility and provenance

- [ ] Produce and verify SLSA-style provenance attestations.
- [ ] Pin and archive builder/run-image manifests and buildpack-order metadata.
- [ ] Make build secret references immutable/versioned in Iteration 5.
- [ ] Add multi-architecture OCI index support.
- [ ] Define rebuild-on-base-image-change as a new candidate build, never a mutation of an existing artifact.

## Deliberately deferred

The following belong to later domains rather than unfinished Iteration 3 work:

- application/environment/release and Argo/PaaSApp delivery — Iteration 4;
- runtime secrets, managed services, domains, and TLS — Iteration 5;
- build usage rating, plans, quotas, and invoices — Iteration 6;
- MCP scopes, approval gates, and autonomous build budgets — Iteration 7.
