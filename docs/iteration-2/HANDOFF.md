> **Historical Iteration 2 snapshot.** The failed gate recorded below was repaired during Iteration 3. See [COMPATIBILITY_PATCH_FOR_ITERATION_3.md](COMPATIBILITY_PATCH_FOR_ITERATION_3.md) and `COMPATIBILITY_PATCH_RESULT.json`.

# Iteration 2 handoff

Machine verdict: **FAIL**

## Delivered

- Source Control domain model and public v1 contracts;
- idempotent GitLab repository provisioning saga;
- GitLab REST v4 adapter and deterministic REST contract tests;
- HMAC/replay-protected webhook boundary with legacy mode opt-in;
- duplicate, missing, delayed, and out-of-order event recovery;
- safe isolated Git workspace with traversal/symlink guards and force-with-lease;
- lost-response recovery for provider provisioning and Git push;
- dedicated PostgreSQL source schema, migrations, optimistic locking, inbox/outbox, audit immutability;
- Source HTTP API;
- unit, concurrency, fuzz, real-local-Git acceptance, race, migration, and tagged PostgreSQL suites;
- implementation record, ADRs, threat model, verification report, and explicit TODO.

## Iteration ownership

Any later-discovered Source Control defect belongs to an Iteration 2 patch. A failing Platform Kernel guarantee belongs to an Iteration 1 patch. Iteration 3 must consume only the immutable SourceRevision contract and must not reach into Source Control tables or GitLab-specific internals.
