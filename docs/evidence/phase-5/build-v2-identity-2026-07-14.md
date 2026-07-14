# Build v2 canonical identity — 2026-07-14

Status: `LOCAL_GREEN`; execution and provider-live build gates remain pending.

Implemented:

- `BuildSpec` has one canonical representation used by both its fingerprint and
  the v2 build identity.
- The source SHA is normalized to lowercase and must be an exact 40- or
  64-character hexadecimal commit identity.
- Context root and definition path are relative, traversal-safe canonical
  paths. The early `source_root` spelling remains an additive compatibility
  input and canonicalizes to `context_root`.
- Platform and secret-reference order is normalized and duplicates are
  removed. Build argument maps are cloned and deterministically encoded.
- The early `network_policy` spelling remains an additive compatibility input,
  while canonical output uses `network_profile`. Conflicting values are
  rejected.
- Network profile, resource class, cache scope and timeout participate in the
  identity. The default resource class canonicalizes to `standard`.
- `ComputeBuildV2Identity` binds API version, project ID, repository ID, exact
  commit and the canonical explicit spec. This prevents same-tenant
  cross-repository artifact reuse.
- Canonicalization clones maps and slices, so a caller cannot mutate identity
  inputs after validation.

Executable evidence:

- `TestBuild_IdentityIncludesCommitAndCanonicalBuildSpec`
- existing build-domain identity and path-normalization regression suite
- `go test ./...`
- `go vet ./...`
- scoped build/contracts/pivot/architecture race tests
- pivot matrix regeneration and `--check`

Matrix effect:

- B1 is now `REUSED` executable domain evidence.
- The complete matrix remains mapped at 157 requirements.
- Discovered Go test/fuzz targets: 908.
- Statuses: `REUSED 47`, `NEW 44`, `LIVE_ONLY 66`.

Not claimed:

- The production build entrypoint does not yet accept/persist this v2 spec;
  B2–B4 remain the next application integration slice.
- No BuildKit VM, registry, SBOM, scanner, signature or provenance live gate is
  marked green by this contract-only slice.
