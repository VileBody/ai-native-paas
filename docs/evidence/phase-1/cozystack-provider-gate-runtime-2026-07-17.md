# Cozystack provider_gate runtime evidence — 2026-07-17

## Scope

Provider-gate validation for the cheap two-IPv4 Cozystack runtime cell:

- `off → provider_gate` recreation with three private Talos/Cozystack nodes;
- Cozystack v1.5.0 platform install through the pinned `cozy-installer` chart;
- trimmed provider-gate package surface for networking, LINSTOR, PostgreSQL,
  Redis and bucket/S3 controllers;
- LINSTOR-backed `replicated` StorageClass on the 260 GiB data disk;
- minimal CNPG PostgreSQL lifecycle and PVC lifecycle smoke;
- zero-drift checks for `network-foundation` and `cozystack-lab`.

## Infrastructure transition

The previous smoke compute was destroyed through OpenTofu and `cozystack-lab`
was recreated as `provider_gate`.

Provider-gate node resources:

```text
cp-1 = 8622881, private IPv4 192.168.74.11
cp-2 = 8622879, private IPv4 192.168.74.12
cp-3 = 8622877, private IPv4 192.168.74.13
cpu_per_node = 8
ram_mb_per_node = 24576
system_disk_mb = 81920
data_disk_mb = 266240
public_ipv4s = 0
```

Talos bootstrap generation 10 completed through the admin Kubernetes fallback
job. The temporary provider transport was removed immediately after evidence was
fetched:

```text
TALOS_BOOTSTRAP_JOB=PASS generation=10
TALOS_BOOTSTRAP_EVIDENCE=PASS generation=10
```

The public edge port check from the admin host-network source showed only the
intended ports:

```text
open=80
open=443
open=6443
closed=50000
closed=50011
closed=50012
closed=50013
```

## Cozystack install

The installer chart was rendered locally:

```text
cozy-installer chart: ghcr.io/cozystack/cozystack/cozy-installer:1.5.0
chart digest: sha256:5c2392d79b8ace58e78403eaad814c5cee27e2a8148cb1fa5be883bfd0306c0a
rendered manifest sha256: 808b9ad9e37de84080a3a939b92975df67f80544487bc3225df9ebccedcd65a2
```

The first provider-gate package set was too broad for the cheap profile:
`tenant-application`, `velero`, `backupstrategy-controller` and
`cozystack-basics` pulled in tenant/monitoring dependencies that are explicitly
outside the cheap provider lifecycle window. The profile was trimmed to the
actual provider surface, while keeping required lightweight dependencies
`prometheus-operator-crds` and `victoria-metrics-operator`.

After trimming stale kept `Package` CRs from the earlier attempt, all active
packages became Ready:

```text
cozystack.backup-controller           True
cozystack.bucket-application          True
cozystack.cert-manager                True
cozystack.cozy-proxy                  True
cozystack.cozystack-engine            True
cozystack.cozystack-platform          True
cozystack.cozystack-scheduler         True
cozystack.flux-plunger                True
cozystack.flux-shard-operator         True
cozystack.gateway-api-crds            True
cozystack.gateway-application         True
cozystack.ingress-application         True
cozystack.kubeovn-plunger             True
cozystack.kubeovn-webhook             True
cozystack.linstor                     True
cozystack.linstor-scheduler           True
cozystack.metallb                     True
cozystack.multus                      True
cozystack.networking                  True
cozystack.objectstorage-controller    True
cozystack.postgres-application        True
cozystack.postgres-operator           True
cozystack.prometheus-operator-crds    True
cozystack.redis-application           True
cozystack.redis-operator              True
cozystack.reloader                    True
cozystack.seaweedfs-application       True
cozystack.snapshot-controller         True
cozystack.victoria-metrics-operator   True
```

All active `HelmRelease` resources reported `Ready=True`; the parent release
ended with:

```text
cozy-system/cozystack-platform True
Helm upgrade succeeded for release cozy-system/cozystack-platform.v4
```

Runtime nodes:

```text
talos-78b-p6f   Ready   192.168.74.13   EXTERNAL-IP <none>
talos-myy-zwk   Ready   192.168.74.12   EXTERNAL-IP <none>
talos-tan-fax   Ready   192.168.74.11   EXTERNAL-IP <none>
```

CoreDNS was already reconciled to `cozy.local` in generation 10:

```text
kubernetes cozy.local in-addr.arpa ip6.arpa
```

## Storage evidence

Applied:

```text
.state-backend/cozystack-provider-gate-linstor-storage.yaml
sha256=244a100eb23ec77d8a11185ae0f95cd47d82e11209a75e4ea42fbb756e1f4649
```

LINSTOR matched all three satellites:

```text
linstorsatelliteconfiguration/ai-native-paas-provider-gate-data-pool
APPLIED=True MATCHED=3
SATELLITES=["talos-78b-p6f","talos-myy-zwk","talos-tan-fax"]
```

`StorageClass/replicated` was created as default:

```text
provisioner=linstor.csi.linbit.com
reclaimPolicy=Delete
volumeBindingMode=WaitForFirstConsumer
allowVolumeExpansion=true
```

PVC smoke:

```text
Namespace provider-gate-storage-smoke created
PVC replicated-smoke-pvc Bound
PV pvc-86059078-7a35-40ad-89d0-5d35ebfacd24 Bound
Pod replicated-smoke-write Completed
```

The first writer pod showed successful PV attach but failed because the selected
probe image could not write to the mounted filesystem as its default user. The
pod was replaced with a root/fsGroup smoke writer; the second run completed.
The smoke namespace was deleted after evidence collection.

## Provider lifecycle evidence

PostgreSQL was verified through the underlying CloudNativePG operator on the
`replicated` StorageClass:

```text
cluster.postgresql.cnpg.io/postgres-smoke
instances=1
ready=1
status="Cluster in healthy state"
primary=postgres-smoke-1

pod/postgres-smoke-1 1/1 Running
pvc/postgres-smoke-1 Bound storageClass=replicated
```

The `apps.cozystack.io/v1alpha1` `Redis` and `Bucket` API resources exist and
accept manifests, but in the trimmed cheap profile they do not reconcile in a
plain namespace. Cozystack's public API documentation shows managed applications
are intended to be created in tenant namespaces. The tenant application stack
requires `etcd-application`, `info-application` and `monitoring-application`;
Velero additionally requires `monitoring-agents`. These remain intentionally
disabled in this cheap profile.

Decision recorded from live evidence:

- `provider_gate_cheap` proves Cozystack core, networking, LINSTOR,
  PostgreSQL/CNPG, Redis operator CRDs/controllers, object-storage
  CRDs/controllers and S3/SeaweedFS application definitions.
- Full high-level `apps.cozystack.io` Redis/Bucket lifecycle requires a
  separate `provider_gate_full` or beta/release window with tenant and monitoring
  dependencies enabled.
- The test namespaces were deleted after the run:

```text
namespace "provider-gate-storage-smoke" deleted
namespace "provider-gate-managed-smoke" deleted
```

## OpenTofu zero drift

Saved plans:

```text
.state-backend/network-provider-gate-zero-20260717.tfplan
NETWORK_PROVIDER_GATE_ZERO_DRIFT=PASS

.state-backend/cozystack-provider-gate-zero-20260717.tfplan
COZYSTACK_PROVIDER_GATE_ZERO_DRIFT=PASS
```

The steady state has no bootstrap DNAT and no node firewall rule exposing Talos
port `50000`.

## Local regression

The Cozystack package policy golden test was updated after the reviewed
provider-gate trim and passed:

```text
go test ./test/architecture -run 'Cozystack|ImageLock'
ok github.com/keir-research/ai-native-paas/test/architecture
```

## Remaining provider-gate work

- Add an explicit `provider_gate_full` profile, or reclassify Redis/Bucket
  high-level lifecycle as a beta/release-window gate requiring tenant and
  monitoring dependencies.
- Add executable tests that reject stale kept Cozystack `Package` CRs after
  profile trim.
- Convert the live storage/provider smoke manifests into repeatable scripts
  instead of ad-hoc `.state-backend` artifacts.
- Run backup/restore once the selected backup strategy is explicit: lightweight
  snapshots for cheap windows, Velero for full release windows.
