# Iteration 5 — Test Report

Generated: `2026-07-12T23:43:52Z`

## Summary

```text
Verdict:                     RED
Mandatory TDD parity:         0/0
Attachment top-level tests:   0
Packages exercised:           0
Fuzz targets discovered:      0
Targeted statement coverage:  unknown
Live PostgreSQL:              NOT RUN
Skipped attachment events:    unknown
Failed attachment events:     unknown
```

## Gates

| Gate | Result |
|---|---|
| `gofmt` | MISSING |
| `vet` | MISSING |
| `default_suite` | MISSING |
| `attachments_suite` | MISSING |
| `attachments_race` | MISSING |
| `attachments_shuffle` | MISSING |
| `attachments_coverage` | MISSING |
| `attachments_no_skips` | MISSING |
| `tdd_parity` | MISSING |
| `postgres_tag_compile` | MISSING |
| `no_cross_schema_sql` | MISSING |
| `no_secret_values_in_contract` | MISSING |
| `immutable_runtime_reference_scan` | MISSING |
| `postgres_live` | NOT RUN |

## PostgreSQL evidence

Raw log: `.verification/final/postgres_live.log`

The live suite is expected to cover clean migration install/upgrade, aggregate-plus-outbox atomicity, concurrent command idempotency, optimistic locking, immutable provider identity, immutable published snapshots and append-only audit. Exact executed test names are preserved in the raw log.

## Limits of this report

No claim is made for real Kubernetes, OpenBao, Cozystack, DNS or ACME behavior. Those are explicitly separated into acceptance backlogs.
