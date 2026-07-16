# Resume the Moscow Cozystack smoke lab

## Current checkpoint (2026-07-17)

`network-foundation=live` is provisioned and has zero drift:

- runtime VPC `network-7ae8fa2881384d4db6046b4dd6854b4e`;
- shared router `49de7bfa-90b5-4ca5-b94a-b7d6aa7a1de4` with both runtime and
  workspace SNAT through preserved `72.56.234.22`;
- runtime LB `135509`, private `192.168.74.5`, public `5.42.126.95`;
- only public LB ports `80`, `443`, and source-restricted `6443`.

`cozystack_profile=smoke` currently owns three private-IPv4 nodes and their
40 GiB data disks:

| Node | Server ID | Private IPv4 |
|---|---:|---|
| `cp-1` | `8616377` | `192.168.74.11` |
| `cp-2` | `8616381` | `192.168.74.12` |
| `cp-3` | `8616379` | `192.168.74.13` |

Timeweb automatically assigns provider IPv6 addresses even when no floating
IPv4 is requested. The attached firewalls have default policy `DROP`; no IPv6
ingress rule is declared. No node has floating IPv4, and steady state has no
public Talos `50000` route.

Talos bootstrap completed through the admin Kubernetes fallback job, generation
9. Evidence was fetched and verified in
`.state-backend/cozystack-bootstrap-generation-9-result`; the public Kubernetes
API proof from the admin host-network source returned all three control-plane
nodes through `https://5.42.126.95:6443`.

The smoke runtime is installed and has live evidence in
`docs/evidence/phase-1/cozystack-smoke-runtime-2026-07-16.md`:

- all Cozystack `Package` and Flux `HelmRelease` resources were Ready;
- Envoy Gateway was Programmed on `5.42.126.95`;
- CoreDNS was reconciled to `cozy.local` and LINSTOR CSI became Ready;
- `StorageClass/replicated` uses each node's `/dev/sdb` data disk;
- Argo CD Core synced `Application/hello-go-smoke`;
- the Timeweb-side HTTP probe returned `200 OK` and body `hello-go`;
- 20 sequential GitOps/Envoy `hello-go` runs passed on 2026-07-17.

The temporary provider path used for bootstrap is closed:

- `cozystack-lab` no longer has node firewall rules for port `50000`;
- `network-foundation` no longer has router DNAT rules `50011-50013`;
- both stacks have zero drift in the live smoke variables.

## Re-run Talos bootstrap from admin Kubernetes

Use this only for a deliberate secure re-apply. The preferred steady state is no
public Talos transport. The fallback pairs short-lived provider DNAT with a
host-network admin Kubernetes Job whose init container installs owner-scoped
host `OUTPUT` DNAT rules for UID `999`; the main bootstrap container itself has
no `NET_ADMIN`.

With the Cozystack state credential and state recovery passphrase exported:

```sh
generation=3
./scripts/prepare-talos-bootstrap-bundle.sh \
  "$PWD/infra/stacks/cozystack-lab" \
  "$PWD/.state-backend/cozystack-bootstrap-generation-${generation}.enc" \
  "$PWD/.state-backend/cozystack-bootstrap-generation-${generation}.passphrase"
```

Switch to the `workspace-bootstrap` HTTP-state credential and stage only the
encrypted input plus short-lived signed URLs:

```sh
./scripts/stage-talos-bootstrap-artifacts.py \
  --bundle ".state-backend/cozystack-bootstrap-generation-${generation}.enc" \
  --generation "$generation" \
  --output ".state-backend/cozystack-bootstrap-generation-${generation}.stage.json" \
  --expires-seconds 21600
```

Switch back to the `cozystack-bootstrap` and `network-bootstrap` credentials.
Enable the matching temporary provider rules with exact review:

- in `network-foundation`, set `talos_bootstrap_dnat_enabled=true` and
  `talos_bootstrap_source_cidr=5.129.202.241/32`; the plan must create exactly
  three router DNAT rules from `72.56.234.22:50011-50013`;
- in `cozystack-lab`, set `talos_bootstrap_dnat_enabled=true` with the same
  source CIDR; the plan must create exactly three node firewall rules for
  `50000`.

If the admin worker changes, re-probe the host-network egress address and use
that exact `/32`; never widen it to a shared CIDR.

Export the normal smoke variables; do not print sensitive state values:

```sh
export TF_VAR_project_id=2545534
export TF_VAR_cozystack_profile=smoke
export TF_VAR_lab_enabled=true
export TF_VAR_transition_from_profile=off
export TF_VAR_cost_guard_acknowledgement=CREATE-3X-4VCPU-8GIB-COZYSTACK-SMOKE
export TF_VAR_runtime_vpc_id=network-7ae8fa2881384d4db6046b4dd6854b4e
export TF_VAR_runtime_router_id=49de7bfa-90b5-4ca5-b94a-b7d6aa7a1de4
export TF_VAR_runtime_edge_private_ip=192.168.74.5
export TF_VAR_runtime_ingress_ip=5.42.126.95
export TF_VAR_node_bootstrap_ssh_key_ids='[599464]'
export TF_VAR_bootstrap_runner_enabled=false
export TF_VAR_bootstrap_runner_ssh_key_ids='[]'
export TF_VAR_talos_bootstrap_dnat_enabled=true
export TF_VAR_talos_bootstrap_source_cidr=5.129.202.241/32
```

Create the matching Kubernetes Secret and ConfigMap, render
`deploy/admin/talos-bootstrap-job/job.template.yaml` with the same generation,
and apply it in `ai-native-paas-system`. The Job consumes the encrypted bundle
from the Secret, uses the provider DNAT only through owner-scoped host rules,
uploads encrypted evidence, and waits for its cleanup sidecar to remove the
host rules before completion. For config-only re-apply to an already
bootstrapped cluster, render the documented `ETCD_ALREADY_BOOTSTRAPPED=true`
line in the Job template.

Fetch and verify the evidence without displaying kubeconfig or talosconfig:

```sh
./scripts/fetch-talos-bootstrap-evidence.py \
  --stage ".state-backend/cozystack-bootstrap-generation-${generation}.stage.json" \
  --passphrase-file ".state-backend/cozystack-bootstrap-generation-${generation}.passphrase" \
  --output-dir ".state-backend/cozystack-bootstrap-generation-${generation}-result"
```

After `TALOS_BOOTSTRAP_EVIDENCE=PASS`, destroy exactly the three node firewall
rules and the three router DNAT rules, delete the Kubernetes Job/Secret/ConfigMap,
and call the evidence verifier again with `--delete-staged-artifacts` to remove
both S3 objects.

## Smoke and teardown gates

If re-running from a fresh bootstrap, install only the golden `smoke` package
set, reconcile CoreDNS to `cozy.local` with
`scripts/reconcile-runtime-coredns-domain.sh`, install Envoy Gateway and
one-replica Argo. Require three Ready nodes, PVC/LINSTOR wiring, internal
ingress, zero drift and 20 sequential `hello-go` runs. PostgreSQL/Redis/S3 and
backup/restore belong to `provider_gate`, never this profile.

If the live window must close before those gates, there is no tenant data to
back up at the current checkpoint. Preserve the encrypted OpenTofu state and
operator evidence, select `off`, use `transition_from_profile=smoke`, and
review the teardown before applying. It must destroy compute/disks/firewalls,
then `network-foundation=off` removes only LB/router while both imported IPv4
reservations and VPCs remain protected.

Never switch `smoke` directly to `provider_gate`, never shrink data disks in
place, and never leave Talos API `50000` reachable after a bootstrap window.
