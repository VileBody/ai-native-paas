# Correlated execution audit evidence — 2026-07-14

Evidence classes: `LOCAL_GREEN`, `DB_GREEN`

Requirement: `G28` —
`TestAgent_AuditConnectsIntentTaskCommandsCommitsPlansApprovalsAndRuntime`.

The Agent audit trail now joins Project MCP v2 operations and the v1
compatibility façade in one append-only task/correlation timeline. The public
contract contains a typed allowlist of identifiers rather than arbitrary
arguments or provider payloads.

Validated invariants:

- task creation records the human intent identifier and verified agent/user
  identity;
- successful Project MCP calls contribute project, workspace, command, source
  plan, infrastructure plan, approval and apply identifiers;
- source/build/runtime calls contribute repository commit, build ID, immutable
  artifact digest, deployment/release, GitOps revision and ready endpoint;
- Project MCP scope is rechecked against the durable task's tenant, agent and
  immutable project binding and correlation before evidence is appended;
- evidence replay is deterministic and idempotent, while the same producer
  idempotency key with changed evidence fails with `CONFLICT`;
- raw argv, environment references, credential leases, patches, secret values,
  plan JSON and provider response bodies have no field in the evidence
  contract;
- a cross-surface sentinel embedded in source content and Project MCP secret
  input is absent from responses, task audit, attachment audit and outbox;
- runtime service responses expose the immutable GitOps commit revision used by
  the audit chain instead of relying on a test-only synthesized field;
- PostgreSQL JSONB round-trip preserves the evidence, and the existing
  append-only trigger rejects mutation; migration
  `004_project_bound_tasks.sql` adds the indexed project binding and prevents
  project identity changes.

Verification commands:

```text
go test -count=1 ./...
go test -race -count=1 ./...
go vet ./...
PKG_CONFIG_PATH=/opt/homebrew/opt/libpq/lib/pkgconfig \
  CGO_ENABLED=1 go vet -tags=postgres_integration ./test/integration
go vet -tags=integration_postgres ./internal/source/postgres
make generate-check

# Through an ephemeral port-forward to the isolated user-test PostgreSQL:
PKG_CONFIG_PATH=/opt/homebrew/opt/libpq/lib/pkgconfig CGO_ENABLED=1 \
  go test -race -count=1 -tags=postgres_integration ./test/integration \
  -run '^TestPostgres_Agent' -v
```

The live database run used only
`ai-native-paas-user-test/user-postgres`; the port-forward was closed after the
suite. It did not access the admin managed PostgreSQL.

Matrix result:

- 157 requirements;
- 969 discovered Go test/fuzz targets;
- `REUSED 91`, `NEW 0`, `LIVE_ONLY 66`;
- zero unmapped requirements.

This is not a claim that the controlled beta is release-ready. Live workspace
VM, GitLab.com, initialized OpenBao, Cozystack, Harbor/Argo, capability
providers, DNS/ACME, chaos and restore gates remain separately pending.
