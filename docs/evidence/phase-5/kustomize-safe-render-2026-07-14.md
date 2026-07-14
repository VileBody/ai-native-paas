# Path-safe deterministic Kustomize render — 2026-07-14

Status: `LOCAL_GREEN`.

Validated and canonically mapped:

- The controlled-beta renderer accepts exactly one local Kustomization and a
  bounded list of local YAML resource files.
- Repository roots, Kustomization paths and resource paths are canonicalized;
  absolute paths, traversal, backslashes and symlinks are rejected.
- Remote Git/HTTP resources, plugins, generators and unknown Kustomization
  fields are rejected rather than executed on a control-plane host.
- Kubernetes resources must have `apiVersion`, `kind` and `metadata.name`.
- The same checked-out files render byte-for-byte identically and produce the
  same SHA-256 digest.

Canonical executable evidence:

- `TestGitOps_KustomizeRenderIsDeterministicAndPathSafe`
- `FuzzKustomizePathNeverEscapesRepository`

Matrix effect:

- R3 is now `REUSED` executable integration/fuzz evidence.
- The matrix discovers 926 Go test/fuzz targets: `REUSED 61`, `NEW 30`,
  `LIVE_ONLY 66`.
- The full generated matrix contains no unmapped requirements.

Scope boundary:

- This is the deliberately restricted local-resource subset used by the beta
  generic GitOps path. It does not claim parity with the Kustomize CLI.
- No live Argo CD reconciliation or Cozystack runtime was used, so those gates
  remain `LIVE_ONLY`.
