# agent-api build gateway — 2026-07-17

Evidence class: production control-plane local gate, not a live workspace or
Harbor gate.

`agent-api` production wiring now supports a tenant-scoped Build API bridge for
legacy MCP v1 build tools:

- `platform_request_build`;
- `platform_get_build`;
- lost-response resume by build ID when the caller already has an operation
  identifier.

If `BUILD_API_URL` is absent, requests remain fail-closed through the existing
retryable dependency failure. If `BUILD_API_URL` is present, request-build also
requires immutable material settings:

- `AGENT_BUILD_BUILDER_DIGEST`;
- `AGENT_BUILD_RUN_IMAGE_DIGEST`;
- `AGENT_BUILD_PLATFORM_VERSION`.

The bridge:

- sends service-principal headers and the verified tenant scope to build-api;
- forwards `Idempotency-Key` and `X-Correlation-ID` for build requests;
- maps build-api 4xx responses to non-retryable agent domain errors;
- maps build-api 5xx/429 responses to retryable provider failures;
- exposes a build artifact to the agent facade only when build-api reports a
  `RELEASABLE` artifact.

## Commands

```text
/opt/homebrew/bin/go test ./internal/agent/productiongate ./cmd/agent-api ./test/architecture
```

## Result

```text
ok  	github.com/keir-research/ai-native-paas/internal/agent/productiongate	0.665s
?   	github.com/keir-research/ai-native-paas/cmd/agent-api	[no test files]
ok  	github.com/keir-research/ai-native-paas/test/architecture	1.097s
```

## Still open

- The bridge is still the legacy v1 build request shape. The full beta build
  slice still requires Project MCP `build/v2` BuildSpec, disposable workspace
  VM execution, Harbor digest/SBOM/signature/provenance evidence and runtime
  trust enforcement.
- Runtime deploy, source/project and attachments gateways are still fail-closed
  until their internal service bridges are wired.
