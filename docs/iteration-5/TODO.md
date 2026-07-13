# Iteration 5 — Remaining TODO

## Completed code and database gates — 2026-07-13

- [x] mandatory TDD parity: 68/68;
- [x] format, vet, default, acceptance, architecture and contract suites;
- [x] Attachments race, shuffled and coverage runs;
- [x] tagged PostgreSQL compilation and complete CI live suite;
- [x] clean migration install/upgrade and migration checksum enforcement;
- [x] aggregate/outbox atomicity, idempotency and optimistic locking;
- [x] provider identity, snapshot and audit immutability;
- [x] dedicated Timeweb Kubernetes cluster in Moscow with one worker;
- [x] isolated VPC shared by Kubernetes and the control-plane database;
- [x] private managed PostgreSQL 17 for platform metadata;
- [x] in-cluster PostgreSQL harness representing user-owned infrastructure;
- [x] dedicated Timeweb Container Registry and GitHub Actions delivery path;
- [x] complete live PostgreSQL gate against both database classes with zero
  skipped tests;
- [x] Terraform drift check returns no changes.

## Cloud account organization

- [ ] Move the isolated cluster, VPC, managed database and registry to a
  dedicated Timeweb project when the API token has `project:create` permission.
  The resources do not share a cluster or VPC with `call-analytics-k8s`, but are
  currently billed under the existing `BALOVSTVO` project.
- [ ] Move local Terraform state, which contains generated database credentials,
  to an encrypted remote backend before adding more operators.

## Real-provider acceptance

- OpenBao: auth method, namespace/policy isolation, lease renewal, revoke and HA
  failover.
- Cozystack: PostgreSQL/Redis/S3 create-discover-update-delete semantics and
  ambiguous-timeout recovery.
- DNS provider: authoritative lookup, propagation behavior, DNSSEC/CNAME chains
  and rate limits.
- ACME/cert-manager: issuance, renewal, failed challenge, revocation and issuer
  rate limits.
- External Secrets Operator: refresh, deletion policy, rollout behavior and
  provider outage.
- Provider callback authentication, outage chaos and regional failure tests.
- Kubernetes-backed Runtime/Commerce/Agent end-to-end acceptance.

The Kubernetes-specific cases are enumerated in `KUBERNETES_TODO.md`.

## Production resilience and operations

- retry budgets, circuit breakers and per-provider concurrency limits;
- backup/restore drills and reconciliation after restore;
- high-cardinality metrics, SLOs and alert thresholds;
- secret-rotation and certificate-renewal soak tests;
- domain verification abuse/rate limiting;
- retention/legal-hold policy for managed services;
- operator runbooks for stuck provisioning, binding and TLS states.
