# agent-api commerce gateway — 2026-07-17

## Scope

`agent-api` production wiring now supports a real Commerce API bridge for:

- entitlement checks before mutating MCP v1 tools;
- usage invoice preview reads.

If `COMMERCE_API_URL` is absent, the bridge remains fail-closed and denies
mutations with the existing `network-deferred-v1` decision.

## Change

- Added `productiongate.HTTPCommerce`.
- `cmd/agent-api` wires it from:
  - `COMMERCE_API_URL`;
  - `AGENT_API_SERVICE_PRINCIPAL` defaulting to `agent-api`.
- Requests are sent with verified service headers:
  - `X-Tenant-ID`;
  - `X-Principal-ID`;
  - `X-Principal-Kind: service`.
- The production adapter inventory now includes
  `commerce-http-gateway-or-fail-closed`.

## Commands

```text
/opt/homebrew/bin/go test ./internal/agent/productiongate ./cmd/agent-api ./test/architecture
```

## Result

```text
ok  	github.com/keir-research/ai-native-paas/internal/agent/productiongate	1.122s
?   	github.com/keir-research/ai-native-paas/cmd/agent-api	[no test files]
ok  	github.com/keir-research/ai-native-paas/test/architecture	1.645s
```

## Remaining agent-api production gateway work

- Source/project mutation tools still need Project MCP/internal client
  translation rather than the legacy source-api.
- Build execution should connect to the workspace/BuildKit/Harbor path once
  the trust-chain artifacts are available.
- Runtime deploy/status should connect after the runtime application creation
  contract is wired into project creation.
