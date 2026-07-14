# Build v2 execution selection — 2026-07-14

Status: `LOCAL_GREEN`, `DB_GREEN` for the scoped persistence gate. Live
workspace execution remains a later provider/system gate.

Implemented:

- Added the additive `POST /v2/organizations/{tenant}/builds` request path.
  It accepts either one canonical explicit `BuildSpec`, or an explicitly
  policy-approved auto-detection fallback; it rejects neither/both modes.
- The exact canonical spec, its digest, request contract and fallback policy
  are persisted with the immutable build configuration and survive store
  reloads and retries. Legacy v1 rows preserve their original JSONB shape, so
  ordinary state transitions do not mutate historical build identity.
- An explicit Dockerfile spec deterministically selects `dockerfile-vm` and
  never invokes runtime detection, even when the repository would otherwise
  look ambiguous.
- v2 auto mode is restricted to the optional buildpacks fallback. A detector
  result that attempts to route an implicit Dockerfile is rejected and must be
  replaced by an explicit spec.
- Execution selection is committed atomically with
  `build.execution_selected.v2` and `build.execution.select` audit evidence,
  including selection source, runtime, backend, buildpack, entrypoint,
  bounded detector evidence and spec digest.
- Dockerfile execution is fail-closed behind the
  `disposable-workspace-vm` capability. A generic builder or a possible shared
  privileged Docker socket adapter is rejected before its `Build` method can
  run.
- The disposable VM adapter advertises that capability and receives the
  canonical build arguments, resource class and timeout along with its
  existing restricted-network request. Runtime-cluster credentials remain
  absent.

Canonical executable evidence:

- `TestBuild_ExplicitBuildSpecOverridesRuntimeDetection`
- `TestBuild_BuildpacksRemainOptionalFallback`
- `TestBuild_DockerfileRunsOnlyInDisposableIsolationBackend`

Additional evidence:

- `TestHTTP_RequestV2PersistsExplicitBuildSpec`
- `TestBuildV2_RequiresExclusiveExplicitOrBuildpacksFallback`
- `TestPostgres_V2BuildSpecRoundTripsAndRemainsImmutable`
- `go test ./...`
- `go vet ./...`
- scoped race tests for build application, domain, HTTP, memory, PostgreSQL,
  Dockerfile VM and build v2 contracts
- PostgreSQL-tagged integration package compilation and vet
- pivot matrix regeneration and `--check`

Managed PostgreSQL evidence:

- A statically linked Linux test binary was copied to a temporary unprivileged
  client pod in the admin cluster. The managed PostgreSQL DSN was injected
  directly from the existing Kubernetes Secret and was not printed or written
  to the repository.
- Clean build migrations and the v2 spec round-trip passed against the Timeweb
  managed test database.
- A direct SQL attempt to replace the persisted Dockerfile definition path was
  rejected by the immutable-config database trigger.
- The temporary pod and local Linux binary were deleted after the run.

Matrix effect:

- B2, B3 and B4 are now `REUSED` executable application evidence.
- The complete matrix remains mapped at 157 requirements.
- Discovered Go test/fuzz targets: 914.
- Statuses: `REUSED 50`, `NEW 41`, `LIVE_ONLY 66`.

Not claimed:

- No real Timeweb workspace VM or BuildKit build ran in this slice.
- B5 network probes, B6 resource-exhaustion tests and the remaining build
  supply-chain/provider gates are still pending.
