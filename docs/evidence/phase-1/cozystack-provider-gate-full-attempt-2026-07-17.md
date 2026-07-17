# Cozystack provider_gate_full attempt — 2026-07-17

## Scope

This records the first explicit `provider_gate_full` release-window attempt.
The goal was to validate the tenant-scoped Cozystack managed-service path for
Redis and Bucket, rather than the cheap frequent `provider_gate` path.

## Code baseline

`provider_gate_full` was added as a closed profile:

- same compute shape as `provider_gate`: 3 x 8 vCPU / 24 GiB / 80 GiB system
  disk plus 260 GiB data disk;
- separate explicit acknowledgement:
  `CREATE-3X-8VCPU-24GIB-COZYSTACK-PROVIDER-GATE-FULL`;
- separate chart values:
  `infra/stacks/cozystack-lab/packages/provider-gate-full-values.yaml`;
- separate package allowlist in
  `infra/stacks/cozystack-lab/packages/profile-policy.json`.

The cheap `provider_gate` profile remains the frequent path and still rejects
tenant/monitoring packages that are only allowed in `provider_gate_full`.

## Local gates

```text
tofu -chdir=infra/stacks/cozystack-lab validate
Success! The configuration is valid.

go test ./test/architecture -run 'CozystackPackage|CozystackLab|ImageLock'
ok github.com/keir-research/ai-native-paas/test/architecture
```

## Create plan

Saved plan:

```text
.state-backend/cozystack-provider-gate-full-create-20260717.tfplan
COZYSTACK_PROVIDER_GATE_FULL_CREATE_PLAN=PASS
create_count=19
```

Planned resources:

```text
talos_machine_secrets.cluster[0]
twc_server.node["cp-1"]
twc_server.node["cp-2"]
twc_server.node["cp-3"]
twc_server_disk.data["cp-1"]
twc_server_disk.data["cp-2"]
twc_server_disk.data["cp-3"]
twc_firewall.node["cp-1"]
twc_firewall.node["cp-2"]
twc_firewall.node["cp-3"]
twc_firewall_rule.node_icmp["cp-1"]
twc_firewall_rule.node_icmp["cp-2"]
twc_firewall_rule.node_icmp["cp-3"]
twc_firewall_rule.node_tcp["cp-1"]
twc_firewall_rule.node_tcp["cp-2"]
twc_firewall_rule.node_tcp["cp-3"]
twc_firewall_rule.node_udp["cp-1"]
twc_firewall_rule.node_udp["cp-2"]
twc_firewall_rule.node_udp["cp-3"]
```

The plan contained no `twc_floating_ip` or `floating_ip_id` markers.

## Live attempt result

The Timeweb apply was blocked before any node became usable:

```text
not enough money on account balance for create new server
```

No Talos bootstrap transport was opened, and Talos port `50000` was not exposed.
The failed apply left only `talos_machine_secrets.cluster[0]` in state.

## Cleanup

Saved cleanup plan:

```text
.state-backend/cozystack-provider-gate-full-failed-cleanup-20260717.tfplan
COZYSTACK_PROVIDER_GATE_FULL_FAILED_CLEANUP_PLAN=CHANGES_PRESENT
```

Cleanup apply:

```text
Apply complete! Resources: 0 added, 0 changed, 1 destroyed.
cozystack_profile = "off"
node_ids = {}
node_private_ips = {}
profile_resources.public_ipv4s = 0
```

Post-attempt zero drift:

```text
.state-backend/cozystack-off-after-provider-gate-full-attempt-zero-20260717.tfplan
COZYSTACK_OFF_AFTER_PROVIDER_GATE_FULL_ATTEMPT_ZERO_DRIFT=PASS
```

## Decision

`provider_gate_full` is ready as an explicit, tested IaC/package profile, but
the live Redis/Bucket/backup lifecycle remains blocked by Timeweb account
balance. Keep the cheap `provider_gate` for frequent checks. Re-run
`provider_gate_full` only during a funded release-window, and immediately return
through `off` after collecting evidence.

## Authorized rerun

A second user-authorized rerun was attempted on 2026-07-17 with a fresh plan:

```text
.state-backend/cozystack-provider-gate-full-create-rerun-20260717.tfplan
COZYSTACK_PROVIDER_GATE_FULL_RERUN_CREATE_PLAN=PASS
create_count=19
```

The apply was again blocked before any node became usable:

```text
not enough money on account balance for create new server
```

No Talos bootstrap transport was opened, and Talos port `50000` was not exposed.
The failed apply left only `talos_machine_secrets.cluster[0]` in state. Cleanup
returned the stack to `off`:

```text
.state-backend/cozystack-provider-gate-full-rerun-cleanup-20260717.tfplan
Apply complete! Resources: 0 added, 0 changed, 1 destroyed.
cozystack_profile = "off"
node_ids = {}
node_private_ips = {}
profile_resources.public_ipv4s = 0
```

Post-rerun zero drift:

```text
.state-backend/cozystack-off-after-provider-gate-full-rerun-zero-20260717.tfplan
COZYSTACK_OFF_AFTER_PROVIDER_GATE_FULL_RERUN_ZERO_DRIFT=PASS
```
