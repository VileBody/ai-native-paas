# Pivot baseline provenance and verification

## Provenance

The implementation baseline is commit `440a195`. It contains the cumulative
Iterations 1–7 tree plus the fully reconstructed Iteration 5 bounded context.
The agentic DevOps pivot is additive: the old contracts remain regression
fixtures while v2 Project MCP, workspace, infrastructure and provider
contracts are introduced.

## Verification performed on this exact merged tree

- default Go suite over the exact cumulative tree;
- 68/68 Iteration 5 requirement parity;
- race, shuffle, contract, architecture and API smoke gates;
- live PostgreSQL suites against a Kubernetes user database and private
  managed platform database;
- registry push/pull and Terraform no-drift checks;
- generated pivot matrix containing all 157 requirements and zero unmapped
  entries.

## Current pivot qualification

Local and database baselines are green. Phase 1 added encrypted HTTP-backed
OpenTofu state, three admin system workers and a live NVMe CSI gate. The admin
cluster now has a TLS-only, three-server OpenBao Raft release with immutable
images and retained data/audit PVCs; it remains deliberately uninitialized and
sealed until the 5/3 holder ceremony, so `PROVIDER_GREEN` is still pending. The
same three system workers also run a TLS-authenticated three-replica NATS
JetStream quorum with retained NVMe PVCs; a replicated live publish succeeded,
while application outbox/inbox and failover drills remain pending.

Generic runtime, disposable workspace, initialized OpenBao, Cozystack,
GitLab.com, capability-provider, security and disaster-recovery gates remain
explicit pending dimensions in `verification/status.yaml`; none is inferred
from fake adapters or rendered manifests. The partial Cozystack provider lab was
destroyed on 2026-07-15 after encrypted recovery verification and now defaults
to a zero-resource cost-standby plan until a scheduled live gate.
