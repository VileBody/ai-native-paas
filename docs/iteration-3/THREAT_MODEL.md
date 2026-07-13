# Iteration 3 threat model

## Protected assets

- source code at an exact commit;
- ephemeral Git credentials;
- build-only secrets;
- runtime-secret isolation;
- builder and run-image identity;
- OCI image bytes and digest;
- SBOM, scan, and signature records;
- platform signing key;
- tenant registry namespaces;
- build logs and cache;
- PostgreSQL build/audit state.

## Trust boundaries

```text
Source Control contract
    │ untrusted repository content
    ▼
source fetch workspace
    │ sanitized exact snapshot
    ▼
build worker / disposable VM
    │ OCI layout
    ▼
registry quarantine
    │ SBOM + scan + signature
    ▼
ArtifactPolicy
    │ ArtifactRef
    ▼
future Runtime Delivery
```

All customer repository content, Dockerfiles, package metadata, symlinks, generated files, and build output are untrusted.

## Threats and implemented controls

### Mutable-source substitution

Controls: exact SHA checkout, post-checkout `HEAD` verification, immutable source contract, no branch resolution in Build.

### Path traversal and host-file disclosure

Controls: canonical relative paths, absolute/`..` rejection, file/byte limits, symlink walk, `source_root` real-path containment, `.git` removal, submodules disabled by default.

### Credential persistence

Controls: ephemeral source credential, credential removal before build, no token in repository URL/config after checkout, cleanup hooks, redaction tests.

### Runtime-secret disclosure

Controls: runtime secrets are outside the build-secret provider interface; tests assert they are absent from requests, logs, OCI layout, SBOM, and metadata.

### Log exfiltration

Controls: streaming redaction before storage, split-write handling, overlapping-secret handling, no echo of invalid request bodies in public errors.

### Cache poisoning or cross-tenant disclosure

Controls: tenant/project scope, exact digest verification, secret-material rejection, separate trusted shared-base scope.

### Registry namespace confusion

Controls: path-segment tenant ownership, immutable digest resolution, invalid-layout rejection, cross-tenant negative tests.

### Signing the wrong object

Controls: signer input is repository plus digest, verifier runs before persistence, signature record binds exact artifact digest, signature attachment digest is required, unknown issuers rejected.

### Trust-record tampering

Controls: append-only audit, immutable scan/signature rows, immutable SBOM and artifact identity, database trigger requiring a complete chain before `RELEASABLE`.

### Duplicate execution and race conditions

Controls: deterministic identity, tenant-scoped uniqueness, idempotency records, Serializable transactions, optimistic versions, race tests, concurrent PostgreSQL tests.

### Dockerfile privilege escalation

Controls present at the contract level: dedicated VM backend, no runtime-cluster credentials, restricted egress profile, guaranteed destruction paths.

Production residual: the actual KubeVirt/Kata/gVisor implementation and node/network policy have not been exercised in this local package.

## Residual risks before public exposure

- live kpack/Paketo and Harbor behavior is not proven here;
- signing uses an in-process Ed25519 test implementation rather than KMS/HSM/cosign key custody;
- scanner is deterministic policy logic, not a live Trivy database/update path;
- no production sandbox, cgroup, seccomp, egress, PID/inode, or wall-clock enforcement was available locally;
- no production crash-resume reconciler exists for partially completed builds;
- no multi-architecture image build;
- package-manager dependency installation and malicious dependency behavior need isolated live tests;
- durable log retention and deletion policy are not wired;
- real registry replication/failover and database failover need chaos tests.

These items are release blockers for an untrusted public build service, but they do not invalidate the Iteration 3 domain contract.
