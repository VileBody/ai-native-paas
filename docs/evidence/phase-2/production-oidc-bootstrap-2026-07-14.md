# Production OIDC bootstrap — 2026-07-14

Evidence class: `DB_GREEN` (including `LOCAL_GREEN` regression evidence).

The production entrypoints for `project-api`, `kernel-api`, `source-api` and
`commerce-api` now use one fail-closed OIDC bootstrap path. Each entrypoint:

- requires `OIDC_ISSUER`, `OIDC_CLIENT_ID` and PostgreSQL;
- serializes the `platform_identity` migration with a PostgreSQL advisory lock;
- performs OIDC discovery and creates a JWKS-backed verifier before serving;
- supplies that verifier to `httpauth.Middleware`;
- records the JWKS verifier and PostgreSQL membership resolver in its
  production adapter inventory.

Validated invariants:

- production tenant requests are no longer rejected merely because the API
  declared identity support without constructing a verifier;
- a missing issuer, client ID or database prevents startup;
- two bootstrap attempts migrate the membership schema exactly once;
- architecture evidence prevents any of the four entrypoints from dropping
  the shared bootstrap or middleware wiring;
- tenant membership roles are not promoted to the global `platform-admin`
  role. Commerce admin routes remain fail-closed until a separate trusted
  platform-role contract is implemented.

Canonical executable evidence:

- `TestNewPostgresVerifierRejectsIncompleteProductionConfiguration`;
- `TestProductionHumanAPIEntrypointsBootstrapPostgresOIDC`;
- `TestPostgres_ProductionOIDCBootstrapMigratesMembershipsBeforeDiscovery`;
- `TestPostgres_OIDCSubjectRequiresExactlyOneActiveBetaMembership`.

Verification:

```text
go test -count=1 ./...
go test -race -count=1 ./...
go vet ./...
PKG_CONFIG_PATH=/opt/homebrew/opt/libpq/lib/pkgconfig CGO_ENABLED=1 \
  go vet -tags=postgres_integration ./test/integration
go vet -tags=integration_postgres ./internal/source/postgres
make fmt-check generate-check

# Through an ephemeral port-forward to the isolated user-test PostgreSQL:
PKG_CONFIG_PATH=/opt/homebrew/opt/libpq/lib/pkgconfig CGO_ENABLED=1 \
  go test -race -count=1 -tags=postgres_integration ./test/integration \
  -run '^TestPostgres_(ProductionOIDCBootstrap|OIDCSubject)' -v
```

The live database run used only
`ai-native-paas-user-test/user-postgres`; the port-forward was closed after the
test. It did not access the admin managed PostgreSQL.

Matrix result:

- 157 requirements;
- 973 discovered Go test/fuzz targets;
- `REUSED 91`, `NEW 0`, `LIVE_ONLY 66`;
- zero unmapped requirements.

This evidence uses a local deterministic OIDC discovery server plus live
PostgreSQL. GitLab.com login, real beta memberships and provider credential
rotation remain a separate provider gate; they are not claimed green here.
