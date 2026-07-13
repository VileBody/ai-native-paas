# Iteration 4 — Threat model

## Protected assets

- tenant isolation;
- production traffic routing;
- immutable release identity;
- registry digest integrity;
- GitOps repository integrity;
- runtime-cell capacity;
- Kubernetes credentials;
- audit and deployment history;
- external attachment references.

## Trust boundaries

```text
Build ArtifactPolicy → Runtime control plane
Runtime control plane → private GitOps repository
Argo CD → PaaSApp
PaaS operator → generated child resources
Kubernetes status → Runtime control plane
```

Every arrow is treated as a validation boundary.

## Principal threats and mitigations

### Mutable-image substitution

Threat: a tag changes after release creation.  
Mitigation: only exact SHA-256 digests are accepted and rendered; mutable tags are absent from runtime manifests.

### Forged or stale Ready status

Threat: a stale PaaSApp generation or another release's status activates the wrong deployment.  
Mitigation: validate object metadata, complete immutable spec, exact image, identity and observed generation before accepting status.

### GitOps path escape

Threat: crafted tenant/application/environment identifiers write outside the owned cell path or through a symlink.  
Mitigation: canonical DNS-safe names, traversal rejection, symlink checks, fuzzing and deterministic path tests.

### Argo privilege expansion

Threat: a renderer or repository compromise creates cluster roles or arbitrary workloads.  
Mitigation: restricted AppProject allows only Namespace and PaaSApp from one repository into owned namespaces; default project is forbidden.

### Operator privilege expansion

Threat: operator credentials expose Secrets or namespace mutation.  
Mitigation: RBAC has no Secret access, no Namespace mutation and no wildcard resources/verbs; generated pods have restricted security settings.

### Cross-tenant status/API access

Threat: a caller reads or mutates another tenant's deployment by ID.  
Mitigation: tenant identity comes from the authentication boundary/path match, and status retrieval validates deployment ownership. Process smoke proves cross-tenant denial.

### Previous-backend spoofing

Threat: a candidate rollout preserves traffic to a Service that merely has the expected name.  
Mitigation: the operator verifies Service port, release label and application label before reusing the previous backend.

### Migration replay

Threat: retry reruns a successful schema migration.  
Mitigation: status records `MigrationCompletedReleaseID`; reconciliation skips the Job for that release once completed.

### Partial Git/DB commit

Threat: Git push succeeds but the control-plane transaction fails.  
Mitigation: deterministic release trailers/path and `FindByRelease` reconcile the already-created Git commit.

### Persistence-column drift

Threat: JSON aggregate says one digest/configuration while indexed SQL columns say another.  
Mitigation: PostgreSQL consistency triggers reject insert/update mismatch; live tests intentionally attempt the attack.

### Runtime-cell over-allocation

Threat: concurrent deployments reserve more capacity than available.  
Mitigation: serializable transaction, optimistic cell version and deterministic baseline-unit accounting.

### Destructive cascade

Threat: deleting ApplicationSet or an app accidentally removes retained external data.  
Mitigation: ApplicationSet preserves resources; runtime deletion is two-phase; attachments are opaque references and are never purged here.

## Residual risks requiring Kubernetes

The following cannot be proved by object-model tests alone:

- actual RuntimeClass presence and gVisor execution;
- admission-controller enforcement;
- server-side apply ownership/conflicts;
- Gateway API routing and status;
- Cilium egress behavior;
- Kubernetes finalizers, watch loss and controller restart semantics;
- real HPA interaction;
- image-signature admission.

They are enumerated as mandatory gates in `KUBERNETES_TODO.md`.
