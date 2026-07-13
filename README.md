# AI-native PaaS — final project snapshot

Cumulative source snapshot of the seven-iteration architecture:

1. Platform Kernel
2. Source Control
3. Build & Artifact Supply Chain
4. Runtime Delivery
5. Application Attachments
6. Commercial Governance
7. Agent Governance & Public API

## Included directly in the Go module

- full cumulative implementation for Iterations 1–4, 6 and 7;
- the frozen, rich `attachments/v1` public contract used by Agent Governance;
- MCP/OpenAPI contracts, PostgreSQL migrations, Argo CD manifests, CRD/RBAC, TDD documents and test suites.

## Iteration 5 recovery note

The earlier Iteration 5 delivery artifact omitted most source files. Its full implementation was later restored and tested, but the unarchived working tree was lost during a sandbox restart before final packaging. Every surviving source fragment is preserved in:

```text
recovery/iteration-5-restored-source-fragments.tgz
```

The original Iteration 5 documentation artifact is also preserved in `recovery/`. See `docs/FINAL_SNAPSHOT.md` for exact provenance and test status.

## Quick checks

```bash
go test ./internal/agent/... ./pkg/contracts/agent/v1 ./pkg/contracts/attachments/v1
go test ./test/architecture
go test ./test/acceptance -run 'TestAcceptance_Agent'
```

PostgreSQL-tagged suites require `TEST_POSTGRES_DSN`. Kubernetes/provider acceptance remains an infrastructure gate.
