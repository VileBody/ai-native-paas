# agent-api kernel operation gateway — 2026-07-17

## Scope

`agent-api` production wiring now supports a tenant-scoped Kernel API bridge for
MCP v1 operation reads and cancellation:

- `platform_get_operation`;
- `platform_cancel_operation`.

If `KERNEL_API_URL` is absent, the bridge remains fail-closed and the MCP
facade returns the existing retryable dependency failure instead of pretending
that operations are available.

## Change

- Added `productiongate.HTTPOperations`.
- `cmd/agent-api` wires it from:
  - `KERNEL_API_URL`;
  - `AGENT_API_SERVICE_PRINCIPAL` defaulting to `agent-api`.
- Requests use service-principal headers:
  - `X-Tenant-ID`;
  - `X-Principal-ID`;
  - `X-Principal-Kind: service`;
  - `X-Scopes: kernel.operation.read kernel.operation.cancel`.
- Cancellation sends an idempotency key derived from the operation ID.
- The bridge verifies the returned Kernel operation still belongs to the
  requested tenant before exposing it to the agent facade.
- The production adapter inventory now includes
  `kernel-http-operation-gateway-or-fail-closed`.

## Commands

```text
/opt/homebrew/bin/go test ./internal/agent/productiongate ./cmd/agent-api ./test/architecture
```

## Result

```text
ok  	github.com/keir-research/ai-native-paas/internal/agent/productiongate	0.968s
?   	github.com/keir-research/ai-native-paas/cmd/agent-api	[no test files]
ok  	github.com/keir-research/ai-native-paas/test/architecture	1.115s
```

## Remaining operation-gateway work

- Live admin-cluster service discovery and mTLS between `agent-api` and
  `kernel-api` still require the deployment manifests/mesh wiring.
- Kernel operation graph, checkpoints and cancellation propagation already have
  executable domain coverage, but the full production crash-replay and NATS
  integration gate remains part of `OPS_GREEN`.
