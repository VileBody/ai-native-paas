# Harbor OCI referrer adapter — 2026-07-17

## What is now executable locally

`internal/build/harbor.Registry` implements the narrow OCI Distribution path
used by a verified build receipt:

```text
exact repository@sha256 digest resolve
-> verify returned Docker-Content-Digest and media type
-> upload SBOM/signature/provenance blobs
-> publish an OCI 1.1 referrer whose subject is that exact artifact digest
```

The registry port now requires `PublishedArtifact` as the attachment subject.
An attachment stored merely in the same repository is no longer considered
trust evidence for a releasable artifact.

The adapter enforces all of the following before it writes a referrer:

- HTTPS registry base URL and a non-empty scoped Harbor robot credential;
- exact `registry/tenants/<tenant>/apps/...` ownership, not substring
  matching;
- immutable valid subject digest and successful subject resolve;
- blob digest checks and an immutable digest returned by Harbor for the
  referrer manifest;
- no OCI-layout copy from the control plane: the disposable workspace must
  publish the image itself.

## Verification

```text
/opt/homebrew/bin/go test ./internal/build/harbor ./internal/build/...
```

Result: pass. The adapter tests use a TLS OCI Distribution test server and
assert the subject digest embedded in the referrer manifest, tenant-path
rejection, basic robot authentication, and every blob/manifest request.

## Explicit limits

This is transport and contract evidence, not a Harbor live gate. The
production `build-api` continues to fail closed because a real project-scoped
Harbor robot lease, OpenBao-backed signing/provenance keys, scanner, SBOM
runtime and disposable workspace receipts have not yet been configured. No
artifact is marked releasable from this adapter alone.
