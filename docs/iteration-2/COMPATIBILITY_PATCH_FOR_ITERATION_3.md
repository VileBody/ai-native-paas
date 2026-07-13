# Iteration 2 compatibility patch discovered during Iteration 3

## Status

**GREEN against the current cumulative repository.**

The archived Iteration 2 package contained a failed machine verdict. The defects were repaired as Iteration 2 work before the Build domain was allowed to depend on `SourceRevision`.

## Repairs

1. Corrected an invalid mixed named/unnamed parameter declaration in Source Control application ports.
2. Removed stale imports and restored clean `gofmt`/`go vet` output.
3. Fixed workspace deletion using a stale optimistic-lock version.
4. Corrected GitLab merge-request REST decoding for snake_case fields.
5. Corrected `force-with-lease` behavior when the target branch does not yet exist.
6. Corrected repository provider-path assignment without changing the immutable numeric provider identity.
7. Removed an architecture-test helper collision.

## Revalidation

The repaired Source Control code passed:

- default tests;
- race-detector suite;
- repeated/shuffled real Git workspace and acceptance tests;
- all tagged Source PostgreSQL tests against PostgreSQL 18.4.

The live Source PostgreSQL suite covered clean migration install/upgrade, atomic project/repository/outbox creation, concurrent idempotency, optimistic branch-head updates, webhook deduplication, altered-payload rejection, out-of-order pushes, append-only audit, immutable provider identity, and bounded Serializable retries.

## Ownership

These changes remain an Iteration 2 patch. No GitLab-specific or Source Control persistence logic was moved into Iteration 3. The Build domain consumes only the frozen `pkg/contracts/source/v1.SourceRevision` contract.
