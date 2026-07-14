# Workspace manager image evidence — 2026-07-14

Evidence tier: `LOCAL_GREEN` (build and supply chain); deployment remains
blocked until the OpenBao initialization ceremony and workspace image import.

- Git revision: `419b5ac548e85388441e43c386fada79c7cd2067`
- GitHub Actions workflow: `workspace-manager`
- Run: `29303755639`
- `verify`: success
- PostgreSQL workspace integration: success
- OCI image build/push: success
- Immutable digest:
  `sha256:49096a554d1d8f4d0b6212bf12ac31c547ef4fb5671af6b559e3cf78027a3691`
- Registry: private Timeweb Container Registry
- Build attestations: BuildKit `provenance=mode=max` and SBOM enabled

The first attempt at revision `7889984` built successfully but could not create
a new GHCR package with the repository workflow token (`write_package`
denied). The production registry is Timeweb, so run `29303755639` removed the
unnecessary GHCR destination and published the same exact-SHA supply chain to
the canonical private registry. No permission was broadened.

This digest is locked in
`deploy/admin/workspace-manager/images.lock.json`. It is not deployed yet:
OpenBao is intentionally sealed/uninitialized, and production workspace
creation must remain unavailable until a signed Timeweb VM image ID and digest
are locked together.
