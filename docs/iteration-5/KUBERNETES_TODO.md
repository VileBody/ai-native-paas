# Iteration 5 — Kubernetes-only Acceptance Backlog

These checks cannot be proven by pure Go tests or PostgreSQL tests.

1. ExternalSecret materializes only in the intended application/environment namespace.
2. Secret values never appear in PaaSApp, Argo CD diff, events or operator logs.
3. ExternalSecret deletion/recreation follows the intended retention policy.
4. Snapshot change triggers a controlled rollout of every process that consumes it.
5. Failed secret refresh does not replace a working Kubernetes Secret with an empty value.
6. gVisor workloads can read bound secrets but cannot access control-plane/provider credentials.
7. NetworkPolicy permits only the bound managed-service endpoints.
8. PostgreSQL/Redis/S3 DNS endpoints resolve from the runtime cell and are denied from unrelated tenants.
9. HTTPRoute is not admitted before certificate readiness.
10. cert-manager renews a certificate without route downtime.
11. Domain removal detaches HTTPRoute before certificate/claim cleanup.
12. Namespace deletion/finalizers do not accidentally purge retained managed services.
13. Operator restart/relist resumes pending binding and snapshot publication exactly once.
14. Multiple operator replicas preserve optimistic/idempotent behavior under leader failover.
15. Argo sync of a same-image/new-snapshot release produces the expected rollout and status.
