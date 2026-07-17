# Project MCP production audit wiring — 2026-07-17

## Scope

`project-api` now wires Project MCP v2 successful tool executions into the
agent task audit chain in production.

Before this change, the handler supported `Audit` injection and tests covered
it, but the production composition root did not provide the recorder.

## Change

- `cmd/project-api` migrates and opens the agent PostgreSQL store.
- The composition root creates an `agentapp.Service` limited to task evidence
  recording dependencies: agent store, clock and ID generator.
- `projectmcp.Handler.Audit` is set to that service.
- Production adapter inventory now includes:
  - `agent-postgres-store`;
  - `project-mcp-agent-task-evidence-recorder`.

## Commands

```text
/opt/homebrew/bin/go test ./cmd/project-api ./test/architecture
```

## Result

```text
ok  	github.com/keir-research/ai-native-paas/cmd/project-api	0.609s
ok  	github.com/keir-research/ai-native-paas/test/architecture	1.084s
```

## Remaining production control-plane work

- Replace fail-closed service gateways in `agent-api` with real internal
  service clients once mTLS/service mesh wiring exists.
- Complete GitLab bot/OIDC live recovery gates.
- Complete OpenBao/NATS HA and crash replay gates.
