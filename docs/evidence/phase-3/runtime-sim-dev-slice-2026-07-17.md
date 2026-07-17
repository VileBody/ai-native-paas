# runtime_sim_k8s development deploy slice — 2026-07-17

## Scope

This evidence records the first executable runtime development slice using
`runtime_sim_k8s` as the default non-production runtime cell.

This is development product evidence only. It does not close
`COZYSTACK_LIVE_GREEN` or `PROVIDER_FULL_GREEN`.

## Changes under test

- `cmd/runtime-api` defaults its non-production runtime cell to:
  - `cell_id=runtime-sim-k8s`;
  - `region=eu1`;
  - `gitops_repository=https://git.example.invalid/platform/runtime-sim.git`;
  - `argo_project=runtime-sim-k8s`;
  - `ingress_domain=sim.runtime.internal`.
- `scripts/runtime-api-smoke.sh` always rebuilds `cmd/runtime-api` before the
  smoke run and verifies the simulator GitOps path/hostname.

## Command

```text
./scripts/runtime-api-smoke.sh .verification/runtime-sim-smoke-20260717-rerun
```

## Result

```text
health: PASS
tenant-derived: PASS
gitops-committed: PASS
runtime-sim-cell: PASS
runtime-sim-hostname: PASS
status-readable: PASS
cross-tenant-denied: PASS
RESULT: PASS
application_id=app-1
environment_id=env-2
deployment_id=dep-8
```

## Verified product slice

```text
runtime-api process
-> create application/default environment
-> deploy releasable artifact
-> local GitOps commit
-> cells/runtime-sim-k8s/... path
-> generated hostname under sim.runtime.internal
-> runtime status read
-> cross-tenant status denial
```

## Remaining DEV_PRODUCT_GREEN work

`DEV_PRODUCT_GREEN` is still pending until the slice is expanded to the full
product path:

```text
Create Project -> GitLab/MCP -> plan -> approval -> build -> GitOps ->
runtime_sim_k8s -> probe -> usage/audit -> destroy
```

The acceptance-level product chain is now recorded separately in
`dev-product-acceptance-slice-2026-07-17.md`; live workspace VM, Harbor trust
chain and Argo/Cozystack evidence are still pending.
