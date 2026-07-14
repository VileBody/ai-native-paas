# Dockerfile immutable base policy — 2026-07-14

Status: `LOCAL_GREEN`. Workspace-agent revalidation and live BuildKit policy
remain pending.

Implemented:

- Every Dockerfile selected by the build service now passes a preflight policy
  before execution selection is persisted or the disposable VM backend is
  called.
- External `FROM` references must use exact `@sha256` digests. `scratch` and a
  previously declared local stage alias are allowed; mutable tags and
  unresolved build ARG bases are rejected.
- Dockerfile paths must remain inside the fetched context. Empty, missing,
  oversized and symlinked definitions fail closed.
- The parser handles case-insensitive instructions, `--platform` options and
  continued logical lines while keeping a one MiB input bound.
- Artifact creation continues to require an immutable output digest; a tag
  such as `latest` cannot become a production artifact identity.
- The application integration test proves a mutable base is rejected before
  the disposable VM builder receives any request.

Canonical executable evidence:

- `TestBuild_MutableBaseOrOutputTagCannotDefineProductionArtifact`

Additional evidence:

- `TestBuildV2_MutableDockerfileIsRejectedBeforeDisposableVM`
- `TestDockerfilePolicy_RejectsDefinitionSymlink`
- `go test ./...`
- `go vet ./...`
- scoped race tests for application, Dockerfile policy and Dockerfile VM
- pivot matrix regeneration and `--check`

Matrix effect:

- B11 is now `REUSED` executable policy evidence.
- The complete matrix remains mapped at 157 requirements.
- Discovered Go test/fuzz targets: 922.
- Statuses: `REUSED 56`, `NEW 35`, `LIVE_ONLY 66`.

Not claimed:

- The same parser is not yet embedded in the remote workspace agent; that is
  required to close the final time-of-check/time-of-use boundary.
- No live rootless BuildKit build ran in this slice.
