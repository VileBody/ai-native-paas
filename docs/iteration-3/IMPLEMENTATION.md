# Iteration 3 implementation — Build & Artifact Supply Chain

## Result

Iteration 3 turns an exact immutable `SourceRevision` into a policy-checked, signed OCI artifact. It deliberately stops before application releases, Argo CD, Kubernetes, service bindings, and billing.

```text
SourceRevision
    ↓ exact checkout
runtime detection
    ↓
buildpacks boundary or Dockerfile-VM boundary
    ↓
OCI image layout
    ↓
tenant registry repository + immutable digest
    ↓
SBOM → policy scan → platform signature
    ↓
ArtifactRef + RELEASABLE decision
```

## Frozen boundary for Iteration 4

The only deployment-facing artifact identity is:

```go
type ArtifactRef struct {
    ArtifactID string
    Repository string
    Digest     string
    MediaType  string
}
```

`ArtifactPolicy.IsReleasable` reloads the persisted artifact, re-resolves the exact registry digest, requires the complete scan/SBOM/signature chain, and cryptographically verifies the persisted signature. Mutable tags are never an authority.

## Bounded-context ownership

The Build domain owns PostgreSQL schema `build` and the following concepts:

- deterministic build identity;
- build state and attempts;
- selected execution backend;
- artifact identity and trust state;
- scan and signature records;
- idempotency, outbox, audit, and log references.

It does not read Source Control tables. Its input is the versioned `SourceRevision` value object. Cross-domain SQL foreign keys and direct internal-package imports are blocked by architecture tests.

## Deterministic build identity

Identity includes:

```text
project_id
repository_id
commit_sha
canonical source_root
normalized build config
builder digest
run-image digest
platform build version
```

Configuration normalization rejects path traversal, absolute paths, NUL bytes, sensitive values embedded in normal build environment variables, duplicate/invalid secret references, and mutable builder/run-image identifiers.

Concurrent requests for the same identity converge on one build record. Successful releasable artifacts can be reused without executing the pipeline again.

## Build state machine

```text
QUEUED
  → FETCHING_SOURCE
  → DETECTING
  → BUILDING
  → EXPORTING
  → SCANNING
  → SIGNING
  → SUCCEEDED
```

Terminal alternatives:

```text
FAILED_USER_CODE
FAILED_PLATFORM
CANCELED
SUPERSEDED
TIMED_OUT
```

The selected runtime/backend/buildpack is persisted while the build is in `DETECTING` and becomes immutable afterward. Cancellation uses that persisted backend; a backend cancellation failure does not falsely mark the build canceled.

## Artifact state machine

```text
DISCOVERED → QUARANTINED → SCANNED → SIGNED → RELEASABLE
                         └──────────────→ REJECTED
```

A releasable artifact must have:

- valid immutable OCI digest;
- immutable SBOM digest and media type;
- passing scan result with policy version and findings digest;
- signature bound to the exact artifact digest;
- persisted signature attachment digest;
- successful cryptographic verification.

PostgreSQL triggers independently reject mutation of artifact identity, build identity, execution selection, SBOM references, scan records, signature records, audit records, and incomplete transitions into `RELEASABLE`.

## Source fetch boundary

The exact-source fetcher:

- resolves a tenant-scoped repository;
- uses an ephemeral credential;
- checks out the exact commit SHA rather than a mutable branch;
- confirms the resulting `HEAD`;
- removes `.git` before build execution;
- disables submodules by default;
- enforces file-count and byte limits;
- rejects symlinks escaping the checkout;
- separately rejects a `source_root` that resolves through a symlink outside the checkout;
- cleans credentials and temporary files.

A monorepo acceptance fixture proves that only the chosen `source_root` is built while a deliberately broken repository root is ignored.

## Runtime detection

The detector supports the zero-config boundaries for:

- Go via `go.mod`;
- Node.js via `package.json`;
- Python via `pyproject.toml` or supported dependency metadata;
- Dockerfile via the advanced isolated backend.

Ambiguous projects require explicit configuration. Unsupported projects receive stable user-facing failure codes.

## Build backends

### Buildpacks/kpack boundary

The kpack adapter models exact-revision creation, lifecycle-status mapping, digest collection, and ownership-scoped cancellation. The local release package does not claim a live kpack cluster integration; that remains an external TODO.

### Dockerfile VM boundary

Dockerfile builds use a separate `dockerfile-vm` port. Tests prove that requests require an isolated disposable VM profile, restricted egress, no runtime-cluster credentials, and VM destruction on success and failure. The release package contains the boundary and deterministic fake, not a production KubeVirt implementation.

### Local deterministic OCI builder

A local OCI-layout builder exists solely for deterministic integration and acceptance tests. It compiles/syntax-checks fixture applications, creates OCI config/manifest/layers, and produces reproducible digests for identical inputs. It is not a replacement for production Paketo/kpack dependency installation.

## Secrets

Runtime secrets never enter the Build domain. Build secrets are resolved through a dedicated port and filtered by allowed phase.

Controls include:

- sensitive values forbidden in ordinary `BuildEnv`;
- only secret references participate in config;
- runtime secret provider data is not requested;
- secrets absent from final OCI metadata and cache metadata;
- streaming log redaction handles secrets split across multiple writes and overlapping secret values;
- redaction is applied before bytes enter the log store.

Iteration 5 must make secret references immutable/versioned so build identity cannot silently resolve to different secret material.

## Registry and trust adapters

The local registry adapter provides:

- exact tenant repository ownership using path segments, not prefixes;
- OCI layout validation;
- immutable digest resolution;
- SBOM/signature attachment storage;
- rejection of cross-tenant repository access.

The scanner produces a deterministic findings digest and maps severity thresholds into a policy result. The signer uses Ed25519 in tests and signs `repository@digest`, never a tag. Verification rejects an unknown issuer or a signature bound to another digest.

## Cache

Cache entries have explicit scope:

- tenant/project cache cannot cross tenant boundaries;
- only trusted base-layer entries may be shared;
- digest mismatch rejects poisoned entries;
- secret-bearing payload or metadata is not stored.

## Persistence

Five checksummed, embedded PostgreSQL migrations create and harden schema `build`. Transactions run at Serializable isolation with bounded retries for serialization/deadlock/unique-winner races.

The adapter stores build and artifact aggregates, idempotency results, outbox events, audit records, scan records, signature records, and log references. Deep-copy behavior in the memory adapter prevents caller mutation from bypassing aggregate invariants during tests.

## HTTP surface

`cmd/build-api` exposes development endpoints for:

- request build;
- get build;
- run build;
- cancel build;
- retry build;
- read logs;
- health check.

It enforces tenant/path consistency, stable public errors, unknown-field rejection, a strict body limit, exact-one-JSON-value decoding, correlation IDs, and idempotency keys.

The packaged binary intentionally wires an in-memory store and health/development surface. Production wiring for PostgreSQL, kpack, Harbor, scanner, signer, secret broker, authentication, rate limiting, and durable logs remains explicit TODO.

## Events

Iteration 3 emits versioned outbox topics:

```text
build.requested.v1
build.started.v1
build.completed.v1
build.failed.v1
artifact.releasable.v1
artifact.rejected.v1
```

Build failures distinguish user code, policy rejection, platform failure, timeout, cancellation, and supersession. Retry retains the original correlation chain.
