# Registry recovery and artifact trust gate — 2026-07-14

Status: `LOCAL_GREEN`, `DB_GREEN` for the scoped managed PostgreSQL gate. Live
Harbor recovery remains pending.

Implemented:

- A registry publish error no longer automatically causes a duplicate rebuild
  or push. When the builder supplied a valid immutable manifest digest, the
  service performs exact `repository@sha256` discovery.
- Recovery succeeds only when repository, digest and media type match the
  expected immutable artifact. A missing or mismatched result preserves the
  original failure path.
- Successful lost-response recovery emits
  `build.registry_publish_recovered.v2` and immutable
  `registry.publish.recover` audit evidence before the trust pipeline
  continues.
- The registry integration test uses the real content-addressed local OCI
  adapter. Its wrapper stores the artifact and then simulates a connection
  reset, proving that the service resolves and records one logical artifact
  without calling publish again.
- The releasability gate was exercised end to end against PostgreSQL. Image
  digest alone, digest plus SBOM, and digest plus SBOM plus a passing scan all
  remain denied. Only the immutable matching signature followed by the domain
  release transition makes the artifact releasable.
- A direct SQL transition to `RELEASABLE` without trust records is rejected by
  the database trigger.

Canonical executable evidence:

- `TestBuild_RegistryPushResponseLostRecoversByDigestDiscovery`
- `TestBuild_TrustChainRequiredBeforeArtifactReleasable`

Additional evidence:

- existing registry digest-resolution and tenant-ownership tests
- existing artifact domain immutability tests
- `go test ./...`
- `go vet ./...`
- scoped build and registry race tests
- PostgreSQL-tagged integration package compilation and vet
- pivot matrix regeneration and `--check`

Managed PostgreSQL evidence:

- The clean build migration gate and the canonical trust-chain test passed
  against the Timeweb managed test database from a temporary unprivileged
  client pod.
- The DSN was injected directly from the existing Kubernetes Secret and was
  not printed or written to the repository.
- The temporary pod and local Linux test binary were deleted after the run.

Matrix effect:

- B9 and B10 are now `REUSED` executable evidence.
- The complete matrix remains mapped at 157 requirements.
- Discovered Go test/fuzz targets: 916.
- Statuses: `REUSED 52`, `NEW 39`, `LIVE_ONLY 66`.

Not claimed:

- No live Harbor push failure was injected in this slice.
- Multi-arch publication, provenance and the malicious-build isolation suite
  remain pending.
