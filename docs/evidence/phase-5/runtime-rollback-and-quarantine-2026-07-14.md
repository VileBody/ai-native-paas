# Runtime rollback revision and unknown-object quarantine — 2026-07-14

Status: `LOCAL_GREEN`.

Validated and canonically mapped:

- Rollback creates a new release identity linked to the historical target; it
  reuses the exact immutable artifact rather than rebuilding or mutating the
  cluster directly.
- The rollback is rendered and committed through a real local Git repository.
  Its persisted record binds release, deployment, path, manifest hash and a
  non-empty commit SHA.
- The accepted rollback also carries immutable actor-scoped
  `runtime.release.rollback` audit evidence.
- An observed runtime object that cannot be matched to a known deployment is
  persisted in quarantine instead of being adopted into a tenant.

Canonical executable evidence:

- `TestRuntime_RollbackCreatesAuditableGitRevision`
- `TestRuntime_UnknownObservedObjectIsQuarantined`

These behaviors already existed under legacy test names. This slice strengthens
the rollback assertions and maps the real executable tests to R8 and R16
without adding duplicate facade tests.

Matrix effect:

- R8 and R16 are now `REUSED`.
- The complete matrix remains mapped at 157 requirements and 924 discovered Go
  test/fuzz targets.
- Statuses: `REUSED 60`, `NEW 31`, `LIVE_ONLY 66`.

Not claimed:

- No live Argo CD reconciliation or Cozystack runtime object was used.
