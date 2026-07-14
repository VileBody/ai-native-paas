# Exact usage rating arithmetic — 2026-07-14

Status: `LOCAL_GREEN`.

Validated and canonically mapped:

- Production rating multiplies with arbitrary-precision integers and never
  converts usage or prices to floating point.
- Positive and negative corrections use the same half-away-from-zero rule.
- Micro-usage is aggregated before the defined rating stage, preventing
  per-event rounding loss or amplification.
- Results are checked against an independent `big.Rat` reference across fixed
  boundary cases and 5,000 deterministic property cases.
- Values outside signed 64-bit minor units fail with an explicit overflow error
  instead of wrapping.

Canonical executable evidence:

- `TestRating_UsesExactArithmeticAcrossMicroUsageAndLargeQuantities`
- `FuzzRatingExactArithmeticMatchesRationalReference`

Matrix effect:

- C15 is now `REUSED` executable property/fuzz evidence.
- The matrix discovers 931 Go test/fuzz targets: `REUSED 65`, `NEW 26`,
  `LIVE_ONLY 66`, with zero unmapped requirements.
