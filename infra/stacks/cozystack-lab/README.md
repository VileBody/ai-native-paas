# Cozystack lab stack

This is a separate self-managed runtime cell in `BALOVSTVO`; it never installs
Cozystack on the managed k0s admin cluster. The stack has four closed profiles:

| Profile | Nodes | Per-node data disk | Intended evidence |
|---|---|---:|---|
| `off` | none | none | default development state |
| `smoke` | 3 x preset `4803` (4 vCPU / 8 GiB / 80 GiB) | 40 GiB | Talos, CNI, LINSTOR, ingress, Argo and `hello-go` |
| `provider_gate` | 3 x configurator `31` (8 vCPU / 24 GiB / 80 GiB, 1000 Mbps) | 260 GiB | frequent cheap gate: LINSTOR, CNPG PostgreSQL and provider CRD/controller wiring |
| `provider_gate_full` | 3 x configurator `31` (8 vCPU / 24 GiB / 80 GiB, 1000 Mbps) | 260 GiB | short release-window gate: tenant-scoped Redis/Bucket, backup/restore and chaos |

The adjacent `network-foundation` state owns the dedicated VPC
`192.168.74.0/24`, preserved runtime IPv4, shared NAT router and runtime L4 LB.
This state receives only the reviewed VPC ID and edge addresses. Both live
profiles create three private-only control-plane/worker nodes, data disks and
deny-by-default firewalls. Every node uses `mode = "no_nat"`, has no
`floating_ip_id`, and exposes Talos API `50000` only inside the VPC. Talos has
no preinstalled CNI or kube-proxy. Profile sizes are fixed in `main.tf`; tfvars
cannot silently resize them.

All three nodes are control-plane plus schedulable workers for this beta lab.
The `smoke` profile is intentionally below Cozystack's supported production
floor and is valid only for control-plane, ingress, storage wiring and
`hello-go` smoke tests. KubeVirt, nested tenant clusters and heavyweight managed
services are disabled. Cozystack owns CNI, ingress and storage in this cluster.

The cheap `provider_gate` profile keeps the same node size as the full gate but
does not install the tenant, monitoring and full backup application layer. It is
for frequent storage/PostgreSQL/provider-surface checks. High-level
`apps.cozystack.io` Redis and Bucket lifecycle must use `provider_gate_full` or
the beta/release window because those applications reconcile inside tenant
namespaces with monitoring and backup hooks.

`cozystack_profile=off` and `lab_enabled=false` plan zero resources in this
state. A live profile requires `network-foundation=live`, `lab_enabled=true`,
`transition_from_profile=off`, reviewed runtime VPC/edge outputs, and the exact
cost acknowledgement. This prevents accidental spend or a direct resize between
live profiles.

Moving between non-zero profiles always means: backup, select `off`, destroy all
VMs/disks, prove the state contains no compute, then recreate the target
profile. Disks are never shrunk in place. `provider_gate_full` must be treated
as a paid evidence window and returned through `off` after evidence collection.
Switching the adjacent network state back to `off` removes only the paid
LB/router; both imported addresses and both free VPCs remain protected there.

The package surface is fail-closed. `packages/profile-policy.json` is the golden
allowlist for Cozystack `v1.5.0`; render the pinned chart and run
`scripts/validate-cozystack-package-set.py` before any Helm apply. Unknown,
missing, or heavyweight packages fail validation. Argo CD is platform-managed,
single-replica in `smoke`, and is not part of the Cozystack Package list.
The matching chart inputs are `packages/smoke-values.yaml` and
`packages/provider-gate-values.yaml` or
`packages/provider-gate-full-values.yaml`; do not construct a live values file
by subtracting packages ad hoc during an incident.

Timeweb's custom-image API does not expose the Moscow `ru-3` location. Nodes
therefore start once from Ubuntu 24.04 and use cloud-init to download the pinned
Cozystack asset into RAM, verify both compressed and decompressed SHA-256 values,
write the system disk with static BusyBox, and reboot into Talos. The system disk
is selected separately from the profile-specific Cozystack data disk. No interactive SSH
or VNC bootstrap is required.

The operator host never connects directly to private Talos port `50000`. Node
creation first produces the sensitive `bootstrap_bundle_inputs` output. Build
an encrypted, checksummed bundle without printing those values:

```bash
./scripts/prepare-talos-bootstrap-bundle.sh \
  infra/stacks/cozystack-lab /secure/bootstrap-1.enc /secure/bootstrap-1.pass
```

Upload that ciphertext and allocate short-lived signed GET/PUT URLs. If account
balance permits another private VM, a separate reviewed apply may then set
`bootstrap_runner_enabled=true` and run the temporary Moscow preset `5943`
runner at `192.168.74.7`, with no public IPv4, no ingress firewall rules and
egress only to VPC DNS, private Talos API and HTTPS.

When Timeweb refuses the extra runner, use the admin Kubernetes fallback in
`deploy/admin/talos-bootstrap-job/job.template.yaml`. The matching
`network-foundation` state may temporarily create three router DNAT rules on
`72.56.234.22:50011-50013`, and this state may temporarily allow the same
reviewed `/32` source to reach node port `50000`. The Job itself runs on the
admin worker with `hostNetwork`; only its init and cleanup containers get
`NET_ADMIN`, and they install/remove owner-scoped host `OUTPUT` DNAT for UID
`999`. The main bootstrap container has no `NET_ADMIN`, verifies pinned
`talosctl`, applies all three configs securely, bootstraps or re-applies the
cluster and uploads encrypted evidence.

Decrypt and validate the result, then immediately destroy exactly the temporary
node firewall and router DNAT rules, delete the Kubernetes Job/Secret/ConfigMap
and revoke staged artifacts. Cozystack install may continue only after both
OpenTofu states report zero drift with `talos_bootstrap_dnat_enabled=false`.
