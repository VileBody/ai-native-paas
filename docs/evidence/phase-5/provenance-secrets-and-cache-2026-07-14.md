# Provenance, secret sentinel and cache isolation — 2026-07-14

Status: `LOCAL_GREEN`. Registry attachment and live BuildKit/workspace gates
remain pending.

Implemented:

- Added a minimal signed in-toto build statement carried in a DSSE-style
  envelope. The signed predicate binds build ID, exact source SHA, canonical
  BuildSpec digest, immutable builder digest, start/finish timestamps,
  repository and output manifest digest.
- Provenance signing uses Ed25519 with an explicit issuer trust map. Verification
  rejects unknown fields, multiple JSON values, unknown issuers, malformed
  statements and any payload/signature mutation.
- Added an integration sentinel test that submits a build-scoped dependency
  secret to the local OCI builder and scans every generated OCI layout file,
  including layer tar, config and history, plus logs, output metadata, SBOM,
  signed provenance and cache behavior.
- The sentinel is absent from every allowed output. A secret-bearing cache
  payload is rejected before persistence.
- Project cache keys bind tenant and project. A unique untrusted layer from
  project A is unavailable to project B and to another tenant, while only the
  explicit `trusted-base` scope can be shared.

Canonical executable evidence:

- `TestBuild_SecretsNeverEnterLayerLogOrProvenance`
- `TestBuild_CacheIsProjectScopedForUntrustedLayers`
- `TestBuild_ProvenanceBindsSourceSpecBuilderAndOutputDigest`

Additional evidence:

- all existing streaming log-redaction tests
- existing cache digest-poisoning and secret-metadata rejection tests
- `go test ./...`
- `go vet ./...`
- scoped race tests for provenance, cache, local OCI, logs and SBOM
- pivot matrix regeneration and `--check`

Matrix effect:

- B7, B8 and B15 are now `REUSED` executable evidence.
- The complete matrix remains mapped at 157 requirements.
- Discovered Go test/fuzz targets: 919.
- Statuses: `REUSED 55`, `NEW 36`, `LIVE_ONLY 66`.

Not claimed:

- Provenance is not yet attached to artifacts in Harbor or enforced by the
  runtime release policy.
- The sentinel test uses the deterministic local OCI integration builder, not
  a live rootless BuildKit process in a Timeweb workspace VM.
