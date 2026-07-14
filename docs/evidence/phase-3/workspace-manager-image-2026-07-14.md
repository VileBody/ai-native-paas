# Workspace manager image evidence — 2026-07-14

Evidence tier: `LOCAL_GREEN` (build and supply chain); deployment remains
blocked until the OpenBao initialization ceremony and workspace image import.

- Git revision: `0940a48f4e1d8348b6b832905e3fe59b243d97e6`
- GitHub Actions workflow: `workspace-manager`
- Run: `29309971206`
- `verify`: success
- PostgreSQL workspace integration: success
- OCI image build/push: success
- Immutable digest:
  `sha256:7b552ce31a42e30d797ad7cb7f313898abe868f950d31a7c445d53e72e9d4fcc`
- Registry: private Timeweb Container Registry
- Build attestations: BuildKit `provenance=mode=max` and SBOM enabled

This revision contains the command-scoped OpenBao credential broker and the
encrypted durable S3 command-log store. The verification job passed race tests
for the manager, workspace adapters and v1 workspace contracts. The PostgreSQL
job passed the workspace migration and lifecycle integration suite against a
clean PostgreSQL 17 service before the image was published.

The first attempt at revision `7889984` built successfully but could not create
a new GHCR package with the repository workflow token (`write_package`
denied). The production registry is Timeweb, so run `29303755639` removed the
unnecessary GHCR destination and published the same exact-SHA supply chain to
the canonical private registry. No permission was broadened.

The current digest is locked in
`deploy/admin/workspace-manager/images.lock.json`. It is not deployed yet:
OpenBao is intentionally sealed/uninitialized, and production workspace
creation must remain unavailable until a signed Timeweb VM image ID and digest
are locked together.
