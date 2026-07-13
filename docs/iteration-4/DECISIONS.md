# Iteration 4 — Architectural decisions

## ADR-4.1 — Argo CD owns only desired PaaSApp state

Argo CD applies Namespace and `PaaSApp`. It never applies generated Deployment, Service, HPA, HTTPRoute, Job or NetworkPolicy objects. This gives every Kubernetes object exactly one authoritative controller.

## ADR-4.2 — A domain-specific operator implements application lifecycle

Application rollout contains migration, readiness, traffic switching, cleanup, suspend and deletion semantics that are richer than a static manifest. These behaviors live in the PaaS operator rather than in Argo hooks or user YAML.

## ADR-4.3 — Exact digest is mandatory

Runtime accepts only a frozen `ArtifactRef` and emits `repository@sha256:...`. Tags are not deployment identity. Rollback reuses an older exact digest and creates a new release record.

## ADR-4.4 — GitOps repository is separate from source repositories

Users and AI agents cannot commit Kubernetes manifests directly. The trusted renderer writes a private cell repository. This preserves the PaaS abstraction and prevents arbitrary cluster-resource injection.

## ADR-4.5 — Status is untrusted input

`PaaSApp.status` is accepted only after metadata, identity, immutable spec, image and generation are checked against runtime PostgreSQL. Kubernetes possession alone does not authorize release activation.

## ADR-4.6 — Placement is sticky

Normal deployments stay in the current cell. Rebalancing and cell migration are explicit operations because state, domain routing and failure domains may be attached to the placement.

## ADR-4.7 — Namespace per application environment

The default unit of runtime isolation and lifecycle is one namespace per application environment. It simplifies quota, deletion, logging attribution and network policy.

## ADR-4.8 — No Tsuru in this path

Runtime Delivery is implemented by the existing GitOps architecture: control plane, Argo CD, PaaSApp operator and Kubernetes. Introducing Tsuru here would create a second owner for applications, releases and child workloads.

## ADR-4.9 — No `client-go` dependency in the prototype

The operator depends on a narrow client port. A raw REST adapter and deterministic fake cover the current scope while keeping the Go module dependency-free. Production adoption may replace the adapter with controller-runtime after live API-server tests justify the dependency.

## ADR-4.10 — Degraded candidate remains recoverable

A transient timeout or workload failure marks the deployment `DEGRADED` but leaves the immutable release `DEPLOYING`. Reconciliation may later recover the same candidate to `READY`; no artificial release needs to be created.

## ADR-4.11 — Attachments are referenced, not owned

Iteration 4 stores `attachmentSnapshotRef` as an opaque immutable reference. Runtime Delivery may mount/use the future snapshot but never provisions or purges the underlying secret, database or domain.
