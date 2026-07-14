# Immutable environment inputs and runtime revisions — 2026-07-14

Status: `DB_GREEN`.

Implemented:

- Strict additive `attachments/v2.EnvironmentInputsSnapshot` validation for
  secret versions, short-lived credential bindings, managed resources,
  capability bindings and signed recipe locks.
- All reference lists must be sorted and unique; the SHA-256 fingerprint binds
  every reference, snapshot identity, environment and publication time.
- Strict JSON decoding rejects unknown plaintext-shaped fields rather than
  silently preserving them.
- PostgreSQL migration `005_environment_inputs_snapshots.sql` adds a typed
  payload-consistency trigger and append-only UPDATE/DELETE guard.
- Exact publication replay is idempotent; the same snapshot ID with changed
  references requires a new immutable snapshot.
- Runtime release identity already binds `AttachmentSnapshotRef`, so the same
  image with new inputs produces a distinct auditable GitOps revision.
- Rollback creates a new revision from the historical artifact and historical
  inputs reference together; current unrelated inputs are not mixed in.

Canonical executable evidence:

- `TestInputsSnapshot_IsImmutableAndContainsReferencesOnly`
- `TestInputsSnapshot_SameImageNewInputsCreatesNewRuntimeRevision`
- `TestInputsSnapshot_RollbackRestoresMatchingHistoricalInputs`

Live PostgreSQL environment:

- `ai-native-paas-user-test/user-postgres` in the Timeweb test cluster through
  a temporary local port-forward, closed after the test.
- The admin/control-plane PostgreSQL database was not used.

Matrix effect:

- A5.27, A5.28 and A5.29 are now `REUSED` executable evidence.
- The matrix discovers 936 Go test/fuzz targets: `REUSED 70`, `NEW 21`,
  `LIVE_ONLY 66`, with zero unmapped requirements.

Scope boundary:

- This slice persists and validates the snapshot and binds it to runtime
  revisions. Automated assembly from live OpenBao/Cozystack/capability gateways
  remains part of their respective provider gates.
