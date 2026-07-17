# Isolated managed PostgreSQL gates — 2026-07-16

Evidence tier: `DB_GREEN`.

The Timeweb managed PostgreSQL cluster now has a dedicated logical integration
database, `ai_native_paas_integration_test` (instance `500245`). The existing
control-plane role has explicit grants to production and test databases; the
two databases remain distinct IaC resources.

`scripts/run-postgres-gates-via-admin-cluster.sh` cross-compiles static Linux
test binaries and executes them in an ephemeral pod against the managed
database's private endpoint. The script fails closed when:

- the IaC output for the separate test database is absent;
- the selected database equals the production database;
- the selected name is not lower-case and suffixed `_test`.

The isolated run completed with:

```text
PASS
PASS
POSTGRES_GATES=PASS integration=green k14=green
```

This includes the full PostgreSQL integration suite and executable `K14`
durable operation graph/checkpoint/audit evidence.

Before this isolation existed, an operator invocation ran destructive tests
against the production database and recreated the `state_service` schema.
Encrypted state blobs in S3 were not lost. Startup migrations and configured
namespace ownership rebuilt the metadata; state-service returned health `204`,
the Cozystack state returned `200`, locks worked, and all three refreshed live
OpenTofu plans returned `No changes`. The separate database and fail-closed
runner are the permanent prevention controls.
