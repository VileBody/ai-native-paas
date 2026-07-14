# Project budget concurrency evidence — 2026-07-14

Evidence class: `DB_GREEN`

Requirement: `C6` — `TestQuota_ConcurrentPlansCannotOversubscribeProjectBudget`.

The Commerce PostgreSQL store serializes reservations for a tenant/resource
pair with a transaction-scoped advisory lock while the service calculates
active capacity. The live test configures a project budget of 100 minor units
and races two independent plan reservations of 80.

Expected and observed invariant:

- exactly one reservation succeeds;
- exactly one receives `QUOTA_EXCEEDED`;
- durable `RESERVED`/`COMMITTED` quantity is 80 and never greater than 100.

Verification command (against the isolated Kubernetes user-test PostgreSQL):

```text
CGO_ENABLED=1 go test -race -count=1 -tags=postgres_integration ./test/integration \
  -run '^TestQuota_ConcurrentPlansCannotOversubscribeProjectBudget$'
```

The local regression, vet, and generated TDD matrix checks also pass. This
evidence does not claim a provider-side apply or production admin database gate.
