# Verified multi-arch OCI index — 2026-07-14

Status: `LOCAL_GREEN`. Live Harbor multi-platform publication remains pending.

Implemented:

- Added a deterministic OCI image-index assembler for the v2 build plane.
- Strict production policy requires one valid, immutable and verified manifest
  for every requested platform. A missing, failed-scan, malformed or duplicate
  platform result rejects index creation.
- A failed platform can be omitted only through an explicit `allowPartial`
  policy. The resulting index is annotated as partial and is marked
  `ProductionAllowed=false`; omission is never silent.
- Platform descriptors are canonicalized and sorted, and unexpected platform
  results are rejected.
- A complete amd64+arm64 index is written as an OCI layout, published through
  the real content-addressed local registry adapter and resolved back by its
  exact index digest.

Canonical executable evidence:

- `TestBuild_MultiArchManifestContainsOnlyVerifiedPlatformDigests`

Additional evidence:

- existing registry tenant ownership and digest verification tests
- `go test ./...`
- `go vet ./...`
- scoped multiarch and registry race tests
- pivot matrix regeneration and `--check`

Matrix effect:

- B14 is now `REUSED` executable registry-integration evidence.
- All non-live B-family requirements are executable; B5, B6 and B13 remain
  correctly classified as `LIVE_ONLY`.
- The complete matrix remains mapped at 157 requirements.
- Discovered Go test/fuzz targets: 924.
- Statuses: `REUSED 58`, `NEW 33`, `LIVE_ONLY 66`.

Not claimed:

- The application service does not yet fan out platform builds or attach this
  index to a Harbor artifact.
- No live arm64 worker or Harbor registry was used in this slice.
