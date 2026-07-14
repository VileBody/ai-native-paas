# Secret absence from control-plane persistence — 2026-07-14

Evidence class: `DB_GREEN`.

Requirement: `A5.3` —
`TestSecret_ValueAbsentFromControlPlanePersistenceAndEvents`.

The canonical test executes a complete secret set, rotation and deletion
lifecycle against the live PostgreSQL Attachments store. Two distinct plaintext
sentinels are written only to the external secret-provider test double.

After every lifecycle step the test:

- discovers every table in the PostgreSQL `attachments` schema;
- serializes every complete row, including payload, audit, outbox and
  idempotency data;
- checks structured application logs;
- checks every runtime attachment snapshot;
- verifies neither plaintext sentinel appears on any control-plane surface.

It also confirms the current value exists in the external secret provider after
set/rotation and that both values are absent there after deletion. Public
metadata and snapshot responses contain only names, versions, references and
opaque hashes.

Verification:

```text
PKG_CONFIG_PATH=/opt/homebrew/opt/libpq/lib/pkgconfig CGO_ENABLED=1 \
  go test -race -count=1 -tags=postgres_integration ./test/integration \
  -run '^TestSecret_ValueAbsentFromControlPlanePersistenceAndEvents$' -v

go test -count=1 ./...
go test -race -count=1 ./...
go vet ./...
PKG_CONFIG_PATH=/opt/homebrew/opt/libpq/lib/pkgconfig CGO_ENABLED=1 \
  go vet -tags=postgres_integration ./test/integration
make fmt-check generate-check
```

The live test used only `ai-native-paas-user-test/user-postgres` through an
ephemeral local port-forward, which was closed after the run. It did not access
the admin managed PostgreSQL.

Matrix result:

- 157 requirements;
- 980 discovered Go test/fuzz targets;
- `REUSED 93`, `NEW 0`, `LIVE_ONLY 64`;
- zero unmapped requirements;
- `A5.3` is now mapped to executable PostgreSQL/redaction evidence.

This test proves the control-plane persistence boundary. A real initialized
OpenBao lifecycle and the cross-surface Git/workspace/plan/build/runtime
sentinel test (`A5.7`) remain separate security/provider gates.
