# Iteration 4 → Iteration 5 handoff

Iteration 5 may use only the public runtime contract and opaque attachment reference. It must not import `internal/runtime/...` or read the `runtime` schema.

## Frozen runtime inputs

```go
type DeployRequest struct {
    TenantID       string
    ApplicationID  string
    EnvironmentID  string
    Artifact       buildv1.ArtifactRef
    Configuration  ReleaseConfig
    IdempotencyKey string
    ActorID        string
}
```

`ArtifactRef` remains exact-digest identity. Runtime configuration includes an opaque:

```text
attachmentSnapshotRef
```

## Contract expected from Attachments

Iteration 5 should publish an immutable, tenant-scoped snapshot reference similar to:

```go
type AttachmentSnapshotRef struct {
    SnapshotID   string
    TenantID     string
    ApplicationID string
    EnvironmentID string
    Version      int64
}
```

The runtime consumer needs only a reference and eventually a materialization contract. It must not receive secret values through GitOps.

## Ownership rules

Attachments owns:

- secret metadata and values;
- service instances and bindings;
- credential rotation;
- generated/custom domains and verification;
- attachment snapshot versioning.

Runtime owns:

- the release that points to a frozen attachment snapshot;
- rollout and status of workloads consuming that snapshot.

Deleting or rolling back an application release must not purge a service instance. Destructive service/database actions require their own Attachments operation and later Agent Governance approval.

## Required Iteration 5 consumer tests

- a snapshot reference can be frozen into a release;
- a new snapshot creates a new release identity;
- secret values never appear in `PaaSApp`, Git commit, audit or API response;
- rollback selects the target release's historical snapshot reference;
- attachment rotation does not silently mutate an active release;
- application deletion revokes bindings according to policy but retains external data by default.

## Follow-up ownership

Any failure in Runtime placement, GitOps, operator semantics, status trust or runtime PostgreSQL remains an Iteration 4 defect and must be fixed here rather than worked around in Iteration 5.
