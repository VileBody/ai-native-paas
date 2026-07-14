# Destroy retention policy evidence — 2026-07-14

Evidence classes: `LOCAL_GREEN`, `DB_GREEN`

Requirement: `G20` —
`TestAgent_DestroyWorkflowShowsPlanAndRetainsResourcesByPolicy`.

Resource retention is now an explicit part of a destroy plan instead of an
implicit omission from OpenTofu output. The trusted workspace subcommand reads
the strict, repository-root `platform.yaml/v2` contract before provider
planning and includes its normalized retention rules in the authenticated
plan receipt. Ordinary Project MCP plan arguments cannot inject or override
those rules.

Observed invariants:

- `platform.yaml/v2` rejects unknown, incomplete, duplicate, oversized and
  non-`retain` rules;
- the contract is opened as a bounded regular file with symlink following
  disabled after exact-source and clean-tree verification;
- the mTLS-authenticated plan receipt binds resource identity, external
  identity, policy and reason, and conflicting idempotent replay fails;
- retained resources participate in the canonical plan hash, are durable in
  PostgreSQL, and appear as a separate count and list on the human approval
  summary;
- a resource cannot be present in both `DELETE`/`REPLACE` and `RETAIN`;
- the exact destructive plan, cost reservation and single-use human approval
  remain required before apply authorization;
- migration `005_retained_resources.sql` upgrades an existing schema and
  backfills legacy plan/receipt rows with JSON arrays, not `null`.

Verification commands:

```text
go test ./...
go vet ./...
go test -race ./internal/infrastructure/... ./internal/project/mcp \
  ./internal/workspaceagent ./pkg/contracts/infrastructure/v1 \
  ./pkg/contracts/project/v2 ./test/pivot
./scripts/generate-pivot-tdd-matrix.py --check

# Against ai-native-paas-user-test PostgreSQL through a local port-forward:
go test -tags=postgres_integration ./test/integration \
  -run 'TestAgent_ApprovalIsSingleUseAndBoundToCanonicalPlan|TestPostgres_InfrastructureMigration004BackfillsStartedApply|TestPostgres_InfrastructureMigration005BackfillsRetentionArrays' \
  -count=1 -v
```

The live PostgreSQL run passed on the isolated user-test database. It did not
touch the admin managed PostgreSQL. This evidence does not claim a live
OpenTofu destroy, Cozystack provider retention, or full Timeweb workspace VM
gate; those remain provider/system evidence.
