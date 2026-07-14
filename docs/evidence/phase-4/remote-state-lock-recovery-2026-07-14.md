# Remote-state lock and audited recovery evidence — 2026-07-14

Evidence class: `DB_GREEN`

Requirement: `G17` — `TestInfraState_RemoteLockPreventsConcurrentMutation`.

The governed OpenTofu HTTP state service uses a PostgreSQL transaction-scoped
advisory lock to serialize lease acquisition per state namespace. The live test
races two independent workspace owners for the same production namespace.

Observed invariants:

- exactly one workspace acquires the lease and the contender receives the
  winning lock ID and owner;
- the losing workspace cannot write the encrypted blob while the winner writes
  exactly one state version;
- an active lease cannot be force-recovered;
- an expired lease cannot be silently replaced by a new `LOCK` request;
- stale recovery requires a credential with the recovery policy bit and a
  non-empty operator reason;
- successful recovery writes the old lock, operator identity, reason, and
  timestamp to the durable recovery audit before releasing the lease;
- a new workspace can acquire the namespace only after that audited recovery.

Verification command (against the isolated Kubernetes user-test PostgreSQL):

```text
CGO_ENABLED=1 go test -race -count=1 -tags=postgres_integration ./test/integration \
  -run '^TestInfraState_RemoteLockPreventsConcurrentMutation$'
```

This proves the PostgreSQL lock and recovery policy path. It does not claim a
live Timeweb S3 mutation, an OpenTofu process apply, or production admin database
evidence.
