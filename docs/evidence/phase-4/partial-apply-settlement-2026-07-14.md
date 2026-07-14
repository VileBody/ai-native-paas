# Partial apply settlement and remainder release — 2026-07-14

Evidence classes: `LOCAL_GREEN`, `DB_GREEN`.

Requirement: `C8` —
`TestUsage_PartialApplySettlesCreatedResourcesAndReleasesRemainder`.

Commerce quota reservations now support an additive `SETTLED` state. The
original requested quantity remains immutable, while `settled_quantity` tracks
provider-discovered allocation and `released_quantity` is the unused remainder.
Only the settled quantity continues to count against the project budget.
Quota requests and reservations also carry an immutable `project_id`; capacity
is calculated per project and settlement rejects a sibling-project reservation
even when both projects belong to the same tenant.

`SettleApplyReservation` normalizes and ownership-checks provider observations,
then performs the following in one store transaction:

- append one immutable usage event for each actually discovered resource;
- transition the quota reservation from `RESERVED`/`COMMITTED` to `SETTLED`;
- release the unobserved portion of the reservation;
- persist settlement idempotency, outbox and audit evidence.

Observations are sorted and fingerprinted. A replay with the same key returns
the original settlement and creates no additional usage; a different payload or
second settlement conflicts. The v2 result explicitly reports
`PARTIAL_APPLY_RECOVERABLE`, so orchestration can reconcile or retry without
pretending the failed apply was all-or-nothing.

The canonical test reserves 100 budget units, commits the reservation, observes
only a PostgreSQL resource consuming 40 units after the load-balancer step
failed, and proves:

- one database usage fact is written;
- 40 units remain settled and 60 are released;
- replay does not double-charge;
- a sibling project cannot settle the reservation or consume its budget;
- a new 60-unit reservation succeeds while one additional unit is rejected.

Migration `004_partial_quota_settlement.sql` adds immutable project scope and
`SETTLED` to the PostgreSQL quota schema. A tagged integration test verifies
state, payload quantities and single usage insertion through the production PostgreSQL adapter. It passed
against the isolated `ai-native-paas-user-test/user-postgres` database; no admin
database was used.

Verification:

```text
go test -count=1 ./test/pivot \
  -run '^TestUsage_PartialApplySettlesCreatedResourcesAndReleasesRemainder$' -v
CGO_ENABLED=1 go test -tags=postgres_integration -count=1 ./test/integration \
  -run '^TestPostgres_CommercePartialApplySettlementIsAtomicAndIdempotent$' -v
go test -count=1 ./...
go test -race -count=1 ./...
go vet ./...
make fmt-check generate-check
```

Matrix result:

- 157 requirements;
- 994 discovered Go test/fuzz targets;
- `REUSED 105`, `NEW 0`, `LIVE_ONLY 52`;
- zero unmapped requirements;
- C8 now maps to executable local and PostgreSQL-backed settlement evidence.

Provider discovery delivery (NATS/webhook) is still an orchestration/live gate;
the settlement core and production persistence boundary are implemented here.
