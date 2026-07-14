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

## Resume attempt after balance top-up

At `2026-07-14T02:44Z` the account API reported a positive balance of
`14361.36 RUB`. A fresh encrypted-state plan contained the expected
`17 add / 0 change / 0 destroy` graph. Existing `cp-2` and `cp-3` were read and
were not scheduled for replacement.

Timeweb accepted creation of `cp-1` twice, assigning server identities
`8598033` and `8598049`, but both servers entered terminal provider state
`error` before configuration or cloud-init execution. The second attempt was
deliberately targeted to the server alone, proving that data disks, Talos, and
the rest of the dependency graph were not the cause. The account balance and
hourly charge were unchanged after each failed create.

Both failed servers were deleted by exact provider identity and absence was
verified through the server list API. Because OpenTofu had durably recorded
each partial create before its provider wait was interrupted, the two stale
`twc_server.node["cp-1"]` state instances were removed only after external
absence was proven. Remote state now contains exactly the retained `cp-2` and
`cp-3` server identities; there is no orphan or duplicate `cp-1`.

The blocker is now `PROVIDER_TERMINAL_CREATE_ERROR`, not a local state-lock,
Talos, or cloud-init failure. A third blind create was intentionally not
attempted. Resume requires Timeweb to confirm capacity/account eligibility for
preset `6633` in `msk-1`, or an explicitly approved equivalent preset that
still satisfies the Cozystack lab sizing gate.
