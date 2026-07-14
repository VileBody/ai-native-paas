# Build concurrency coalescing — 2026-07-14

Status: `LOCAL_GREEN`, `DB_GREEN`.

Implemented:

- The atomic persisted `QUEUED → FETCHING_SOURCE` transition is the build
  execution lease. Only the transaction that wins this transition may fetch,
  detect, build and publish.
- A concurrent worker that observes a non-queued state, or loses the start
  compare-and-swap, becomes an observer of the same build operation. It
  returns the current build ID/state without invoking a duplicate backend.
- The bounded PostgreSQL serializable retry budget was raised from 6 to 32
  after the first 20-way managed concurrency run exposed exhaustion under
  legitimate idempotency contention. The existing incremental backoff and
  context cancellation remain in force.
- Twenty concurrent requests use distinct idempotency keys but the same build
  identity; all resolve to one build. Twenty concurrent run calls observe that
  same operation, while the builder executes once, one `build.started.v1`
  event is committed and one artifact is persisted.

Canonical executable evidence:

- `TestBuild_SameIdentityConcurrentRequestsExecuteOnce`

Additional evidence:

- existing optimistic-lock and concurrent identity tests
- `go test ./...`
- `go vet ./...`
- scoped build application/PostgreSQL race tests
- PostgreSQL-tagged integration package compilation and vet
- pivot matrix regeneration and `--check`

Managed PostgreSQL evidence:

- The initial run with the historical six-retry limit failed with a real
  SQLSTATE `40001` retry-budget exhaustion under 20-way contention.
- After the bounded retry-budget correction, the same test passed once and
  then passed three additional consecutive runs against the Timeweb managed
  test database.
- The DSN remained injected from the existing Kubernetes Secret. The
  temporary unprivileged client pod and local Linux test binary were deleted.

Matrix effect:

- B12 is now `REUSED` executable PostgreSQL/concurrency evidence.
- The complete matrix remains mapped at 157 requirements.
- Discovered Go test/fuzz targets: 923.
- Statuses: `REUSED 57`, `NEW 34`, `LIVE_ONLY 66`.

Not claimed:

- This does not implement automatic takeover/resume of a worker that dies
  after winning the start transition; crash recovery remains a separate gate.
