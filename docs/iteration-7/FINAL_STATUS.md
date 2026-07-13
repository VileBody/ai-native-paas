# Iteration 7 / MCP v1 baseline status

Included: 20 versioned MCP tools, strict JSON Schemas, Agent principals, on-behalf-of identity, approval grants, budgets, repair-loop controls, write-only secret semantics, PostgreSQL adapter/migrations, HTTP API and lifecycle acceptance tests.

Local domain, HTTP, contract, architecture and lifecycle acceptance checks are part of the final package verification. Live PostgreSQL and production infrastructure remain explicit external gates.

This is now the compatibility baseline for the agentic DevOps pivot. Project
MCP v2, remote workspaces and OpenTofu/GitOps orchestration are additive work
tracked by `docs/pivot/tdd-catalog.json` and `verification/tdd-matrix.json`.
MCP v1 cannot be removed or used to bypass v2 governance gates.
