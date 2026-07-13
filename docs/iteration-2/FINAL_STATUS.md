> **Historical Iteration 2 snapshot.** The failed gate recorded below was repaired during Iteration 3. See [COMPATIBILITY_PATCH_FOR_ITERATION_3.md](COMPATIBILITY_PATCH_FOR_ITERATION_3.md) and `COMPATIBILITY_PATCH_RESULT.json`.

# Iteration 2 — final machine verdict

**Verdict: `FAIL`**

Verification exit code: `999`

Generated: `2026-07-12T17:20:37.248545+00:00`

## Gate steps

- `gofmt`: **FAIL(1)** — 0
- `vet`: **FAIL(1)** — 1
- `test-default`: **PASS** — 3
- `test-race`: **FAIL(1)** — 25
- `test-shuffle`: **FAIL(1)** — 2

## Failed or incomplete steps

- `gofmt` (FAIL(1)): 0
- `vet` (FAIL(1)): 1
- `test-race` (FAIL(1)): 25
- `test-shuffle` (FAIL(1)): 2

The raw verification log is packaged as `docs/iteration-2/VERIFICATION.log`.
