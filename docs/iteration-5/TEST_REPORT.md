# Iteration 5 — Test Report

Generated: `2026-07-13T20:26:32Z`

## Summary

```text
Verdict:                         GREEN
Mandatory TDD parity:            68/68
Attachment tests discovered:     84
Attachment package tests:        74
PostgreSQL integration tests:     55
Attachments PostgreSQL tests:     9
Live user PostgreSQL in K8s:      PASS
Live managed control-plane PG:    PASS
Skipped live PostgreSQL tests:     0
```

## Gates

| Gate | Result |
|---|---|
| `gofmt` | PASS |
| `vet` | PASS |
| `default_suite` | PASS |
| `attachments_suite` | PASS |
| `attachments_race` | PASS |
| `attachments_shuffle` | PASS |
| `attachments_coverage` | PASS — application package 73.7% |
| `attachments_no_skips` | PASS |
| `tdd_parity` | PASS — 68/68 |
| `postgres_tag_compile` | PASS |
| `architecture_and_contract` | PASS |
| `api_smoke` | PASS |
| `postgres_live_ci` | PASS |
| `postgres_live_user_k8s` | PASS — zero skips |
| `postgres_live_managed` | PASS — zero skips |
| `terraform_drift` | PASS — no changes |

## PostgreSQL evidence

GitHub Actions run
[`29281941776`](https://github.com/VileBody/ai-native-paas/actions/runs/29281941776)
passed the normal verification job, the complete PostgreSQL gate with
`postgresql-client` installed, and the dual-registry test-runner build.

The same runner image then passed `make test-postgres` inside the dedicated
Timeweb Kubernetes cluster against both database classes:

- `iteration-5-postgres-tests` used the namespace-scoped `user-postgres`
  StatefulSet, representing a user-requested database running in Kubernetes;
- `iteration-5-control-plane-postgres-tests` used private managed PostgreSQL
  over the shared VPC, representing platform users, subscriptions and events.

Both runs executed the Attachments clean-install/upgrade, idempotency,
optimistic-locking, provider-identity, audit, payload-consistency, secret-schema
and acceptance tests. The source-provider migration and concurrency tests also
executed; none were skipped.

## Limits of this report

This report proves the recovered Iteration 5 code, PostgreSQL behavior, CI image
delivery and both Kubernetes-to-PostgreSQL paths. It does not claim live
OpenBao, Cozystack, authoritative DNS, ACME/cert-manager or External Secrets
Operator acceptance; those remain explicit provider backlogs.
