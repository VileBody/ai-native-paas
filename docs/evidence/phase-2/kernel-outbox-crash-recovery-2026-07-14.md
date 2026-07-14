# Kernel outbox crash recovery — 2026-07-14

Status: `DB_GREEN`.

Environment:

- PostgreSQL pod behind `ai-native-paas-user-test/user-postgres` in the existing
  Timeweb managed Kubernetes test cluster.
- Local ephemeral port-forward used only for the tagged test and closed after
  completion.
- No control-plane/admin database schema was modified.

Validated:

- Aggregate and outbox event commit in one PostgreSQL transaction.
- A restarted dispatcher discovers the durable pending event after the
  committing process is gone.
- The first delivery commits the consumer inbox/effect, then a simulated crash
  prevents the outbox `PUBLISHED` acknowledgement.
- After lease expiry, another dispatcher redelivers the same immutable event.
- PostgreSQL inbox fingerprinting suppresses the duplicate handler invocation:
  two transport deliveries produce exactly one effect.
- Final database state has one aggregate, one inbox row and one published
  outbox row with a delivery timestamp.

Canonical executable evidence:

- `TestKernel_OutboxCommitThenCrashPublishesExactlyOnceEffect`

Command shape:

```text
go test -race -count=1 -tags=postgres_integration ./test/integration \
  -run '^TestKernel_OutboxCommitThenCrashPublishesExactlyOnceEffect$' -v
```

Matrix effect:

- K8 is now `REUSED` executable PostgreSQL/broker evidence.
- The matrix discovers 932 Go test/fuzz targets: `REUSED 66`, `NEW 25`,
  `LIVE_ONLY 66`, with zero unmapped requirements.

Scope boundary:

- The broker adapter is an in-process deterministic `EventPublisher` exercising
  the production dispatcher/inbox contracts. NATS JetStream live recovery
  remains part of the separate operator/system gate.
