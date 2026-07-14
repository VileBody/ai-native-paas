# Workspace manager image evidence — 2026-07-14

Evidence tier: `LOCAL_GREEN` (build and supply chain); deployment remains
blocked until the OpenBao initialization ceremony and workspace image import.

- Git revision: `3e4dc8774ffe265e25fc92e2cd417393226105bb`
- GitHub Actions workflow: `workspace-manager`
- Run: `29320772358`
- `verify`: success
- PostgreSQL workspace integration: success
- OCI image build/push: success
- Immutable digest:
  `sha256:8de3938abbe3a6bb31bf4cba60e0e36e8ba3ddb64a6c40f1828148bf59ee8312`
- Registry: private Timeweb Container Registry
- Build attestations: BuildKit `provenance=mode=max` and SBOM enabled

This revision contains the command-scoped OpenBao credential broker, the
encrypted durable S3 command-log store, the mTLS egress gateway bootstrap and
explicit trust of the mounted OpenBao CA. It also enforces Timeweb's current
1000 Mbps Moscow configurator minimum. The verification job passed race tests
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
