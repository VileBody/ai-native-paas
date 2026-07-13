# Iteration 5 — Implementation

## Bounded-context responsibility

`attachments` owns the lifecycle and metadata for everything attached to an application environment:

- runtime and build secret metadata;
- references to secret values stored through the secret-provider port;
- managed service instances;
- least-privilege service bindings;
- credential rotation and revocation;
- generated platform domains;
- custom-domain ownership verification;
- TLS issuance state;
- immutable attachment snapshots consumed by Runtime Delivery.

It does not own application source, OCI images, Kubernetes workloads, prices or agent approvals.

## Secret boundary

The API writes a value once to the secret provider and persists only metadata plus an opaque provider reference. Read APIs return names, versions and timestamps, never values. Build secrets and runtime secrets are distinct scopes. Logs, events, audit envelopes and GitOps state are redacted independently of handler behavior.

## Managed-service saga

```text
REQUESTED
  -> PROVISIONING
  -> READY
  -> DEPROVISIONING
  -> RETAINED / DELETED
```

A deterministic provider idempotency key permits recovery when the provider created a resource but its response was lost. Reconciliation first discovers by immutable external identity before retrying creation.

Deleting an application revokes its bindings but retains data services by default. Purge is a separate destructive operation.

## Binding lifecycle

A binding gets the minimum provider role required by its declared capability set. Rotation creates a replacement credential, writes it to the secret provider, publishes a new snapshot, waits for runtime adoption, and only then revokes the old credential. A failed cutover preserves the old usable credential.

## Domain and TLS lifecycle

Custom domains require a unique ownership challenge. Verification performs independent DNS observations separated in time and validates the complete answer set, protecting against transient DNS rebinding. Route activation requires verified ownership and a ready certificate. Domain release enters quarantine before another tenant may claim it.

## Runtime bridge

An attachment change does not mutate a live release in place. It requests a new Runtime Delivery release using:

- the same immutable OCI repository and digest;
- a new immutable `AttachmentSnapshotRef`;
- a new auditable runtime release identity.

Rollback therefore restores both executable artifact and attachment snapshot coherently without rebuilding the image.

## Persistence

The PostgreSQL adapter owns schema `attachments` and enforces:

- tenant-scoped uniqueness;
- optimistic version checks;
- command idempotency;
- atomic aggregate mutation plus outbox;
- append-only audit;
- immutable provider identity;
- immutable published snapshots;
- no cross-domain foreign keys or SQL joins.
