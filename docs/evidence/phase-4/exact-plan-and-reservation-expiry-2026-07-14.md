# Exact-plan approval and reservation expiry — 2026-07-14

Status: `LOCAL_GREEN`.

Validated and canonically mapped:

- An approval grant is bound to tenant, project, plan ID, canonical plan hash,
  estimate version, reservation, target and actor.
- Editing IaC creates a different plan hash; a grant issued for the previous
  plan cannot authorize the replacement plan.
- Apply authorization checks reservation expiry atomically with the persisted
  plan before marking execution as started.
- A late apply with an otherwise valid authorization is rejected and leaves
  `ApplyStartedAt` empty, requiring a fresh plan/estimate/reservation cycle.

Canonical executable evidence:

- `TestCost_ApprovalInvalidatedWhenPlanHashChanges`
- `TestQuota_ExpiredReservationCannotAuthorizeLateApply`

Matrix effect:

- C4 and C7 are now `REUSED` executable domain/application evidence.
- The matrix discovers 928 Go test/fuzz targets: `REUSED 63`, `NEW 28`,
  `LIVE_ONLY 66`, with zero unmapped requirements.

Not claimed:

- This slice uses the deterministic in-memory transactional store. PostgreSQL
  budget contention remains covered by its separate C6 gate.
