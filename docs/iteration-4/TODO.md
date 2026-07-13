# Iteration 4 — Remaining non-Kubernetes TODO

These items do not require a Kubernetes cluster, but they are productionisation work beyond the iteration's tested domain core.

## Control plane

- Replace development principal headers with Kernel/OIDC middleware and scope checks.
- Replace sequential development IDs with production ULID/UUID generation.
- Wire the real Build `ArtifactPolicy` adapter instead of the development environment switch.
- Replace domain-struct HTTP serialization with frozen versioned public DTOs.
- Add durable operation workers for GitOps commit and status reconciliation.
- Add periodic status reconciliation scheduling and dead-letter handling.
- Add API rate limits, request IDs, metrics, traces and structured production logging.

## PostgreSQL

- Wire the runtime PostgreSQL store into `runtime-api`.
- Define pool sizes, statement timeouts, transaction timeouts and retry metrics.
- Add advisory migration locking for multiple API replicas.
- Run HA/failover, connection-loss and deadlock chaos tests.
- Add backup/PITR automation and a restore drill for schema `runtime`.
- Add larger concurrency/soak tests for release identity and placement capacity.

## GitOps

- Implement the remote GitLab repository adapter using short-lived credentials.
- Protect the runtime-cell repository and production branch.
- Sign/verify platform Git commits if required by policy.
- Add remote push ambiguous-timeout and rate-limit tests.
- Add repository mirror/restore tests.

## Operator executable

- Add production config loading for unit catalog and feature flags.
- Add leader election or an explicit single-replica policy before HA rollout.
- Add backoff, jitter and reconciliation metrics.
- Replace polling with watch/informer only after Kubernetes acceptance proves reconnect/resync behavior.

## Operational testing

- Load test deployment creation, reconciliation and status reads.
- Soak test GitOps recovery and runtime status reconciliation.
- Test cell draining and explicit placement migration with realistic state volumes.
- Add incident runbooks for a broken GitOps repo, unavailable registry and stuck deployment.

Everything in `KUBERNETES_TODO.md` is a separate mandatory live-cluster gate, not a duplicate of this list.
