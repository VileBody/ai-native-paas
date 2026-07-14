# Exact-plan approval concurrency evidence — 2026-07-14

Evidence class: `DB_GREEN`

Requirement: `G24` — `TestAgent_ApprovalIsSingleUseAndBoundToCanonicalPlan`.

The live PostgreSQL test creates a production plan and one human grant, then
proves that changed plan hashes and changed agent identities are rejected
without consuming it. Two different apply commands race for the remaining
grant through the production store.

Observed invariants:

- exactly one concurrent command consumes the grant and starts apply;
- the other command receives a conflict and cannot reuse the grant;
- the winning command remains idempotently replayable after response loss;
- the database contains one consumed grant and one started plan;
- approval identity columns remain immutable.

Verification command (against the isolated Kubernetes user-test PostgreSQL):

```text
CGO_ENABLED=1 go test -race -count=1 -tags=postgres_integration ./test/integration \
  -run '^TestAgent_ApprovalIsSingleUseAndBoundToCanonicalPlan$'
```

This is PostgreSQL authorization evidence only; it does not claim an external
OpenTofu apply or provider mutation.
