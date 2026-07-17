# `v0.1.0-beta.1` readiness

Updated: 2026-07-17

The controlled beta is **not released yet**. The repository baseline, isolated
PostgreSQL gates, imported IPv4 foundation, private smoke compute, Talos,
Cozystack, Envoy Gateway, LINSTOR, Argo CD Core and 20 consecutive GitOps
`hello-go` HTTP probes are in place. The release remains blocked by
`provider_gate`, workspace, provider, security, DR and signed-release gates.

ADR 0007 adds an explicit split between the cheap development runtime simulator
and real Cozystack live certification:

```text
DEV_PRODUCT_GREEN       runtime_sim_k8s executable product slice
COZYSTACK_LIVE_GREEN    real Talos/Cozystack runtime cell evidence
PROVIDER_FULL_GREEN     real apps.cozystack.io Redis/Bucket/S3 evidence
```

Simulator evidence is allowed to unblock product development, but it is not
Cozystack live evidence and cannot close release-window provider/security/DR
gates.

## Green evidence

- All 157 pivot requirements are mapped to executable or explicitly
  `LIVE_ONLY` evidence; `K14` and `E2E-1` through `E2E-12` have executable test
  entrypoints.
- PostgreSQL integration and `K14` run in the dedicated managed database
  `ai_native_paas_integration_test`; the runner refuses the production database
  and any database name that does not end in `_test`.
- `network-foundation` owns the two preserved Moscow IPv4 resources, the
  runtime/workspace VPCs, the runtime L4 load balancer, and shared NAT router.
  Live validation and a zero-drift plan pass.
- The runtime edge exposes only `80`, `443`, and source-restricted `6443` on
  `5.42.126.95`. Both private VPCs use `72.56.234.22` for SNAT.
- The smoke profile has three private nodes at `192.168.74.11-13`, each with
  4 vCPU, 8 GiB RAM, an 80 GiB system disk, and a 40 GiB data disk. No floating
  IPv4 is attached to a node.
- Talos bootstrap evidence generation 9 is present locally, the temporary
  bootstrap transport is closed, and Cozystack smoke packages are Ready.
- Envoy Gateway is Programmed on `5.42.126.95`, LINSTOR exposes the default
  `replicated` StorageClass from `/dev/sdb`, and Argo CD Core synced
  `hello-go-smoke` from `codex/runtime-smoke-hello-go`.
- The Timeweb-side public edge probe passed 20 consecutive GitOps runs; each
  run hard-refreshed Argo, waited for `Synced` and `Healthy`, and returned body
  `hello-go` for `Host: hello-go.5.42.126.95.nip.io`.
- A Timeweb admin host-network port check confirmed `80`, `443`, and `6443`
  are open on `5.42.126.95`, while Talos `50000` is closed.
- Production entrypoints fail closed when PostgreSQL, OIDC, or production
  adapters are missing; admin and workspace services use internal
  `ClusterIP` exposure where required by the architecture.
- `runtime_sim_k8s` has a bounded admin-cluster namespace/AppProject manifest
  for cheap executable product slices and is explicitly labeled
  `ai-native-paas.io/evidence-class: dev-product` and
  `ai-native-paas.io/not-cozystack-live: "true"`.
- `runtime-api` now defaults its development cell to `runtime-sim-k8s`; the
  process smoke proves application creation, GitOps commit under
  `cells/runtime-sim-k8s`, generated hostnames under `sim.runtime.internal`,
  status read and cross-tenant denial.
- Acceptance coverage now exercises the cheap dev-product chain through agent
  governance: project create, repository patch, build, exact approval,
  `runtime_sim_k8s` hostname, deployment status, HTTP probe evidence, usage
  preview, workspace destroy evidence and redacted audit.
- Production `project-api` now injects an agent PostgreSQL-backed task evidence
  recorder into Project MCP v2; successful workspace/infra/repository tools are
  no longer unaudited in the production composition root.
- Production `agent-api` now has a tenant-scoped Commerce HTTP bridge for
  entitlement checks and usage preview when `COMMERCE_API_URL` is configured;
  without it, mutations remain fail-closed.
- Production `agent-api` now has a tenant-scoped Kernel operation HTTP bridge
  for operation reads and cancellation when `KERNEL_API_URL` is configured; it
  sends service-principal scopes, idempotent cancellation keys and rejects
  cross-tenant Kernel responses before exposing them through the agent facade.
- Project MCP v2 now wires `build_execute` into the workspace service as a
  governed `workspace-agent verified-build` command bound to the workspace's
  exact source SHA; spoofed source revisions and generic command bypasses fail
  closed before creating a billable command.
- `build_execute` now carries a canonical `build/v2` BuildSpec with server-side
  defaults and a BuildSpec fingerprint in the command serialization key.
  `workspace-agent verified-build` validates exact Git HEAD, a clean source
  tree, immutable Dockerfile bases, the separate verified UID boundary and the
  rootless BuildKit metadata digest path.
- Capability gateway admission now has an executable provider-neutral core:
  project-scoped binding admission, per-project rate-limit integration,
  fail-closed provider lease dependency handling, public output without master
  credentials, and provider usage translation into stable Commerce meters.

## Live checkpoint

- Runtime VPC: `network-7ae8fa2881384d4db6046b4dd6854b4e`.
- Workspace VPC: `network-687cddd36c3147b3bff75c79e9779498`.
- Runtime load balancer: `135541`, public `5.42.126.95`, private `192.168.74.6`.
- Shared NAT router: `49de7bfa-90b5-4ca5-b94a-b7d6aa7a1de4`.
- Smoke nodes: `8616377`, `8616381`, and `8616379`.
- Talos bootstrap result: `.state-backend/cozystack-bootstrap-generation-9-result`.
- Runtime smoke evidence: `docs/evidence/phase-1/cozystack-smoke-runtime-2026-07-16.md`.
- OpenTofu zero-drift refreshed on 2026-07-17 for `network-foundation=live`
  and `cozystack-lab=smoke` after rotating scoped HTTP state credentials.

Timeweb also assigns provider IPv6 addresses to these nodes. Provider
firewalls default to `DROP`; no IPv6 ingress rule is declared. `SECURITY_GREEN`
still requires credential rotation, cross-surface sentinel, and workspace
compromise/isolation evidence.

## PostgreSQL safety incident and remediation

An early destructive integration invocation targeted the production admin
database and recreated the `state_service` schema. State blobs remained intact
in S3; the state-service migrations and configured namespace ownership rebuilt
the metadata, and state reads and locks recovered. A separate managed test
database, an explicit IaC grant, and a fail-closed in-cluster PostgreSQL gate
runner now prevent the same class of mistake.

During final operator verification, the current main S3 credential file was
rendered in local operator output. It remains ignored by Git and the tracked
secret sentinel is green, but the credential must be treated as compromised.
Because Timeweb uses that credential across the state, log and staging buckets,
its coordinated rotation and consumer reconciliation are required before any
security/release gate can become green. Authenticated attempts against the
documented main-user update route returned `404` for `POST`, `PATCH`, and
`PUT`; no credential or consumer was changed. Rotation must therefore be done
through a confirmed Timeweb route or the account panel, followed by automated
reconciliation and old-key rejection evidence.

## Still required

- Run the durable CoreDNS reconcile path on future bootstraps so the runtime
  DNS domain converges to `cozy.local` before LINSTOR gates run.
- Pass the `provider_gate` PostgreSQL/Redis/S3 lifecycle, backup/restore,
  isolation, security, and `E2E-1` through `E2E-12` live gates.
- Pass the `DEV_PRODUCT_GREEN` simulator path:
  `Create Project -> GitLab/MCP -> plan -> approval -> build -> GitOps ->
  runtime_sim_k8s -> probe -> usage/audit -> destroy`.
  The local acceptance slice covers the agent/runtime/audit half, Project MCP
  now queues the governed build command, and workspace-agent has the verified
  rootless BuildKit Dockerfile boundary; GitLab repository bootstrap,
  disposable VM, Harbor receipt ingestion/trust chain and live Argo remain open.
- Pass `COZYSTACK_LIVE_GREEN` and `PROVIDER_FULL_GREEN` in a funded
  `provider_gate_full` window; simulator evidence must not be substituted.
- Complete GitLab, OpenBao holder, capability-provider, beta-domain/DNS, and
  final HA inputs and gates.
- Wire the capability gateway core to real OpenBao/provider leases, live
  OpenRouter/Apify/Bright Data calls, usage replay and provider substitution
  evidence.
- Rotate the Timeweb main S3 secret and reconcile state-service, backend,
  workspace-log and image-staging consumers; prove the previous secret fails.
- Produce signed immutable release artifacts and the final restore drill.

Until those items pass, `DEV_PRODUCT_GREEN`, `PROVIDER_GREEN`, `K8S_GREEN`,
`COZYSTACK_LIVE_GREEN`, `PROVIDER_FULL_GREEN`, `SECURITY_GREEN`, and
`OPS_GREEN` remain pending. The smoke cell and simulator are evidence inputs,
not a beta release.
