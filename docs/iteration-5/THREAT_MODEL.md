# Iteration 5 — Threat Model

## Protected assets

- secret values and provider credentials;
- database/Redis/S3 access grants;
- custom-domain ownership;
- certificate private material;
- tenant and application identity;
- immutable runtime attachment snapshots.

## Principal threats and controls

### Secret exfiltration

Controls: write-only API semantics, opaque provider references, recursive redaction, no secret values in domain events/audit/GitOps, separate build/runtime scopes, no value-returning MCP/API contract.

### Cross-tenant confused deputy

Controls: authenticated tenant context, tenant-bound provider identities, composite uniqueness, authorization before side effects, no caller-supplied provider path.

### Duplicate or ambiguous provider effects

Controls: deterministic idempotency keys, immutable external IDs, discover-before-create reconciliation, optimistic locking and transactional outbox.

### Credential-rotation outage

Controls: create replacement, publish snapshot, confirm runtime adoption, revoke old credential last; failed cutover retains old credentials.

### Destructive data loss

Controls: app deletion only revokes bindings; service purge is separate, approval-bearing and retention-aware.

### Domain takeover and DNS rebinding

Controls: high-entropy challenge, independent observations, complete-answer validation, tenant binding, quarantine after release, TLS readiness before route activation.

### Provider callback spoofing

Controls: provider callback authentication, immutable operation/provider IDs, state-machine validation and replay deduplication.

### Snapshot substitution

Controls: immutable snapshot identity/version, tenant/app/environment binding, runtime release references exact snapshot ID, database immutability trigger.

## Residual risk requiring production acceptance

Real OpenBao policy behavior, Cozystack provider semantics, authoritative DNS propagation, ACME/cert-manager behavior, Kubernetes ExternalSecret reconciliation, network isolation and provider failover remain deployment-environment gates.
