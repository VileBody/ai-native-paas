# ADR 0007: runtime simulator vs Cozystack live certification

Date: 2026-07-17

## Status

Accepted

## Context

The product must become executable before every live infrastructure dependency
is affordable or continuously available. The full Cozystack provider gate needs
three Timeweb VMs plus tenant, monitoring and backup packages. Current Timeweb
account balance blocks that live window, and running Cozystack inside the admin
Kubernetes namespace would be technically dishonest: Cozystack owns
cluster-level concerns such as CNI, storage, tenant model and runtime-cell
assumptions.

The immediate engineering goal is the product path:

```text
Project -> GitLab/MCP -> plan -> approval -> build -> GitOps -> runtime -> probe -> usage/audit
```

That path should be testable without pretending that a namespace-level
simulator is production Cozystack.

## Decision

Add an explicit development runtime driver named `runtime_sim_k8s`.

`runtime_sim_k8s` runs inside the existing admin Kubernetes cluster in a bounded
namespace. It is allowed to prove product orchestration and contract behavior:

- project creation and repository bootstrap;
- MCP tool calls;
- plan, approval and usage/audit flow;
- GitOps commit and Argo sync;
- workload deploy, probe, reconcile and destroy;
- simulated managed Postgres/Redis/S3 lifecycle.

It is not Cozystack evidence and must never close live release gates.

The production/live driver remains `cozystack_live`. It is the only driver that
can close these gates:

- Talos install and bootstrap;
- real Cozystack installer;
- tenant namespaces and `apps.cozystack.io` managed applications;
- LINSTOR on dedicated nodes;
- Timeweb runtime/workspace NAT, firewall and public edge evidence;
- production-grade backup/restore and chaos gates.

The runtime application layer already exposes stable ports for rendering,
GitOps, observing runtime objects and placement. The simulator and live drivers
must share those contracts instead of adding separate product semantics.

## Guardrails

- Simulator manifests must carry `ai-native-paas.io/runtime-driver:
  runtime_sim_k8s` and `ai-native-paas.io/evidence-class: dev-product`.
- Simulator manifests must also carry `ai-native-paas.io/not-cozystack-live:
  "true"`.
- Simulator evidence may close `DEV_PRODUCT_GREEN`; it may not close
  `COZYSTACK_LIVE_GREEN`, `PROVIDER_FULL_GREEN`, `SECURITY_GREEN` or
  release-window DR/chaos gates.
- Cozystack live evidence remains `off -> provider_gate_full -> evidence ->
  backup -> off` during funded windows.
- The legacy VPS is not a Cozystack host. It may only be considered as a
  temporary jump/bootstrap helper if network policy allows it.

## Consequences

Daily development can proceed against a cheap executable slice while preserving
honest release evidence. The beta plan now has two separate success tracks:

```text
DEV_PRODUCT_GREEN       simulator product path is executable
COZYSTACK_LIVE_GREEN    real runtime cell is certified
```

This reduces budget pressure without weakening the controlled beta gate.
