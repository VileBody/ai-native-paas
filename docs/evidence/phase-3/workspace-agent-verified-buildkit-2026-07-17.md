# Workspace-agent verified BuildKit path — 2026-07-17

Evidence class: `DEV_PRODUCT_GREEN` input, not `SECURITY_GREEN` or Harbor live
release evidence.

The `workspace-agent verified-build` subcommand is now an executable boundary
for the Dockerfile build slice:

- Project MCP canonicalizes `build_execute.spec` into `build.platform.example.com/v2`
  `BuildSpec`, defaulting `network_profile=governed`, `cache_scope` and
  `resource_class` where older MCP clients omit them.
- The workspace command serialization key includes both exact source SHA and
  BuildSpec fingerprint, so different build definitions on the same commit do
  not collide.
- The workspace-agent trusted subcommand allowlist includes `verified-build`
  under the separate verified UID, without inheriting command credential FDs.
- `verified-build` revalidates the canonical BuildSpec, requires Dockerfile
  driver, requires the command-bound exact Git HEAD and a clean source tree,
  validates the Dockerfile immutable-base policy, and invokes rootless BuildKit
  through `buildctl --addr unix:///run/workspace-buildkit/buildkitd.sock`.
- BuildKit output is pushed under a temporary deterministic tag, but the only
  artifact accepted from the metadata file is the immutable
  `containerimage.digest`.

The current slice intentionally fails closed for build secret refs until the
BuildKit secret broker and receipt ingestion path are wired.

## Verification

```sh
/opt/homebrew/bin/go test ./internal/project/mcp ./internal/workspaceagent ./test/contract ./test/architecture
```

Result: pass.

## Remaining live work

This does not yet close the Harbor trust-chain gate. The next step is to add
workspace build receipt ingestion so the control plane can persist the digest,
attach SBOM, vulnerability scan result, signature and provenance, then reject
runtime deployment unless the complete immutable trust chain is present.
