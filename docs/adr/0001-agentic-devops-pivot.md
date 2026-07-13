# ADR 0001: Project MCP and remote workspace become the product core

- Status: accepted
- Date: 2026-07-14

## Decision

The primary product path is `Project → Git repository → Project MCP → isolated
remote workspace → plan/approval → OpenTofu and GitOps`. Technology-specific
PaaS aggregates remain optional accelerators rather than mandatory deployment
formats.

Git, encrypted OpenTofu state, OCI digests, provider APIs, OpenBao and the
operation ledger are sources of truth. Shell history and workspace disks are
not sources of truth.

MCP v1 remains available through compatibility handlers, but every v1 mutation
must pass the same scope, cost, approval and audit gates as MCP v2.

## Consequences

- Iterations 1–7 remain regression baselines and bounded-context owners.
- New contracts are additive and versioned.
- A new technology can use generic Helm, Kustomize, OpenTofu or a provider
  driver without a new core aggregate.
- The release is blocked until an external AI agent can deploy without any
  platform administrator credential.
