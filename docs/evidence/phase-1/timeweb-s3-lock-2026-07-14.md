# Timeweb S3 native lock gate — 2026-07-14

Evidence tier: `PROVIDER_GREEN` (negative capability result)

- bucket object versioning: **PASS**;
- sixteen concurrent `PutObject` calls with `If-None-Match: *`: **FAIL**;
- observed winners: **16**; expected winners: **1**;
- native OpenTofu S3 `use_lockfile`: **FORBIDDEN** for this provider endpoint;
- selected fallback: platform HTTP backend with PostgreSQL lock ownership and
  versioned, client-encrypted state blobs in Timeweb S3.

This result is intentionally retained as a gate: S3 backend files are not used
for active stacks unless the conditional-write probe later passes.
