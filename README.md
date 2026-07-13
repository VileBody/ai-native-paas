# AI-native DevOps Platform

This repository is the implementation baseline for an agentic DevOps platform:
a user creates a project, gives a private Git repository and Project MCP URL to
an AI agent, and the platform executes the resulting Git/OpenTofu/build/GitOps
workflow inside governed remote workspaces.

The original seven bounded contexts remain in place:

1. Platform Kernel
2. Source Control
3. Build & Artifact Supply Chain
4. Runtime Delivery
5. Application Attachments
6. Commercial Governance
7. Agent Governance & Public API

## Current baseline

- Iterations 1–7 are present in the cumulative Go module;
- Iteration 5 is fully restored and verified at 68/68 TDD parity;
- all live PostgreSQL suites have passed against both user-owned Kubernetes
  PostgreSQL and private managed control-plane PostgreSQL;
- MCP v1 stays frozen behind governed compatibility routes; the additive v2
  contracts and closed 53-tool Project MCP catalog are executable;
- the accepted pivot specification and 157-requirement catalog live under
  `docs/pivot/`.

The exact migration decisions are recorded under `docs/adr/`. Generated
requirement evidence is stored in `verification/`.

## Quick checks

```bash
make fmt-check
make test
make generate-check
```

## Pivot contracts

- `templates/project-v2/platform.yaml` is the strict project template;
- `contracts/project/v2/platform.schema.json` is its editor-facing schema;
- `contracts/mcp/v2/catalog.json` is the canonical Project MCP tool catalog;
- `contracts/mcp/v2/compatibility-v1.json` documents how every MCP v1 tool is
  forced through v2 scope, policy, idempotency and approval gates.

PostgreSQL-tagged suites require `TEST_POSTGRES_DSN`. Provider, Kubernetes,
workspace-security and resilience statuses are independent live gates; see
`verification/status.yaml`.

## Local PostgreSQL verification

The Docker harness starts PostgreSQL 17 on `127.0.0.1:55432` and runs every
surviving live PostgreSQL suite with the Go race detector:

```bash
make test-postgres-docker
```

The CGO test driver requires the libpq development files (`brew install libpq
pkg-config` on macOS, or `apt-get install libpq-dev pkg-config` on Debian and
Ubuntu). The database remains available after the test run. Manage it with:

```bash
make postgres-up
make postgres-down
make postgres-reset  # also removes the test data volume
```

Override `POSTGRES_IMAGE` or `PAAS_TEST_POSTGRES_PORT` to test another image or
host port.
