> **Historical Iteration 2 snapshot.** The failed gate recorded below was repaired during Iteration 3. See [COMPATIBILITY_PATCH_FOR_ITERATION_3.md](COMPATIBILITY_PATCH_FOR_ITERATION_3.md) and `COMPATIBILITY_PATCH_RESULT.json`.



<!-- FINALIZER-TODO-V1 -->

# Explicit remaining TODO

## Verification failures to repair in Iteration 2

The final gate did not pass. Treat every item below as unfinished Iteration 2 work, not as debt to be hidden in a later iteration:

- [ ] Repair `gofmt` (`FAIL(1)`) — 0
- [ ] Repair `vet` (`FAIL(1)`) — 1
- [ ] Repair `test-race` (`FAIL(1)`) — 25
- [ ] Repair `test-shuffle` (`FAIL(1)`) — 2

## Not proven against external production services

- [ ] Run the GitLab adapter contract suite against the exact target GitLab Self-Managed version, not only the deterministic REST stub.
- [ ] Confirm and freeze the exact webhook signature headers/canonicalization supported by that GitLab deployment; keep legacy `X-Gitlab-Token` opt-in only.
- [ ] Run PostgreSQL tests against the exact production PostgreSQL major version and connection-pool/driver configuration used by the control plane.
- [ ] Exercise GitLab rename, transfer, archived project, protected branch, rate-limit, 429, 5xx, and timeout behavior against a live instance.
- [ ] Exercise Git LFS and explicitly supported submodule policies against a live GitLab instance.

## Security and abuse hardening before public exposure

- [ ] Run workspace workers in a dedicated sandbox with filesystem, inode, PID, CPU, memory, wall-clock, and egress limits.
- [ ] Add repository/LFS size quotas and maximum changed-file/byte limits.
- [ ] Add secret scanning and malware policy for agent-authored commits.
- [ ] Store webhook keys and ephemeral Git credentials through the platform secrets broker; test rotation and revocation under failure.
- [ ] Add API rate limiting and tenant-scoped abuse controls.

## Resilience and scale tests

- [ ] Soak-test concurrent provisioning, webhook storms, reconciliation, and workspace cleanup at expected production cardinality.
- [ ] Inject GitLab latency, connection resets, ambiguous timeouts, database failover, deadlocks, serialization failures, and worker termination.
- [ ] Benchmark indexes and reconciliation claim queries on production-scale tables.
- [ ] Prove backup/restore of the `source` schema and replay of outbox/inbox state.

## Deliberately deferred to later domains

These are not Iteration 2 defects:

- source-to-image build execution and artifact signing — Iteration 3;
- release/deployment/Argo/PaaSApp lifecycle — Iteration 4;
- runtime/build secrets, managed services and domains — Iteration 5;
- quota/rating/billing — Iteration 6;
- MCP scopes, approvals and autonomous action budgets — Iteration 7.
