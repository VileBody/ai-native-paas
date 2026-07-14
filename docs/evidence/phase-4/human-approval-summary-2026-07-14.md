# Human exact-plan approval summary — 2026-07-14

Status: `LOCAL_GREEN`.

Implemented and validated:

- Additive `infrastructure/v1.ApprovalSummary` contract with exact plan,
  project, target, hash, estimate, reservation and approval expiry bindings.
- Deterministic create/update/replace/delete/no-op counts.
- Explicit deletion/replacement risks with the affected resource address.
- Signed monthly deltas: deletion can reduce cost; updates/replacements expose a
  conservative range when before/after quantities are unavailable.
- Separate one-time delta completeness and sorted unknown price components.
- Integer-only markup and overflow-checked aggregation; no floating point.
- The payload contains no raw provider values or credentials.

Canonical executable evidence:

- `TestCost_ApprovalSummaryIncludesDestructionRiskAndMonthlyDelta`

The golden JSON assertion protects the human-facing approval contract from
silently losing the destructive risk, cost delta or exact-plan bindings.

Matrix effect:

- C16 is now `REUSED` executable contract/golden evidence.
- The matrix discovers 929 Go test/fuzz targets: `REUSED 64`, `NEW 27`,
  `LIVE_ONLY 66`, with zero unmapped requirements.

Not claimed:

- Live provider one-time prices are not available in the current pinned
  Timeweb price snapshot; the contract marks those components incomplete rather
  than presenting zero as a known charge.
