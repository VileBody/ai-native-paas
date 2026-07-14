# Cozystack lab bootstrap gate — 2026-07-14

Evidence tier: `PROVIDER_BLOCKED_BALANCE` with partial `K8S_GREEN` bootstrap evidence

## Provider capability result

- requested location: Moscow `ru-3` / `MSK-1`;
- Timeweb custom-image API location enum does not include `ru-3`;
- importing the pinned image in another location returned `no_active_storages`;
- selected fallback: one-shot Ubuntu 24.04 cloud-init bootstrap in Moscow;
- fallback verifies both compressed and raw SHA-256, writes the system disk from
  RAM with static BusyBox, then reboots into Talos without SSH or VNC.

Pinned artifact evidence:

- compressed size: 347477428 bytes;
- compressed SHA-256: `92b0caa5d5cc5f802d042671ccbb4b097c5133dfa9a86c101420761f7e2a0df8`;
- raw size: 4453302272 bytes;
- raw SHA-256: `8b33667aa31957df641e832d5fa4c344f48dbd0c64e784fa98a1dc698d524ecb`;
- private staging cleanup: **0 objects / 0 multipart uploads**.

## Live resources and gate status

- dedicated VPC `192.168.74.0/24`: created;
- `cozystack-cp-2`: Timeweb `on`, Talos API port 50000 reachable;
- `cozystack-cp-3`: Timeweb `on`, Talos API port 50000 reachable;
- both retained nodes have an attached per-node firewall with default policy
  `DROP`, operator-only 50000/6443 ingress and VPC-only TCP/UDP/ICMP;
- first `cozystack-cp-1` attempt entered provider `error` and was removed through
  its exact OpenTofu resource address;
- replacement `cozystack-cp-1` was rejected with
  `not enough money on account balance for create new server`;
- remote state remained locked and consistent throughout recovery.

The three-node HA, data-disk, machine-configuration, etcd bootstrap and Cozystack
lifecycle gates are **not green** until the Timeweb account balance allows the
third 8 vCPU / 32 GiB dedicated node. Existing nodes are intentionally retained
for resume; they are not reported as a working Cozystack cluster.
