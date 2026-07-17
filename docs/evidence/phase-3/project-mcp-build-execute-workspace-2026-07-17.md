# Project MCP build execution wiring — 2026-07-17

Evidence class: `DEV_PRODUCT_GREEN` input, not a live workspace/Harbor gate.

Project MCP v2 now turns `build_execute` into a governed workspace command
instead of returning an unavailable adapter. The handler validates the additive
BuildSpec contract, resolves the workspace's immutable source revision, and
queues only:

```text
workspace-agent verified-build <workspace-source-sha> <canonical-build-spec>
```

The agent-controlled `source_sha` is treated as an assertion, not authority.
If it differs from the workspace-bound exact commit SHA, the request fails with
`POLICY_DENIED` before any billable command is created.

## Guardrails covered

- `workspace_id` must be a platform ID.
- `source_sha` must be lowercase hex and match the workspace source revision.
- `driver` is limited to `dockerfile`, `buildpacks`, `nix`, or
  `custom-approved`.
- build platforms are limited to `linux/amd64` and `linux/arm64`.
- optional definition paths must be clean relative Unix paths.
- secret references remain opaque IDs.
- the workspace service permits `workspace-agent verified-build` only with
  command kind `build_execute`; generic `command` requests and mismatched
  verified subcommands are rejected.

## Verification

```sh
/opt/homebrew/bin/go test ./internal/project/mcp ./internal/workspace
/opt/homebrew/bin/go test ./...
```

Result: pass.

## Remaining live work

This slice does not run rootless BuildKit, does not publish to Harbor, and does
not produce SBOM/signature/provenance evidence. Those remain part of the
workspace/build/release path before `v0.1.0-beta.1`.
