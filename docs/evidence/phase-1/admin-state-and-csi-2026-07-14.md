# Admin state, capacity and CSI gate — 2026-07-14

Evidence tiers: `PROVIDER_GREEN`, `K8S_GREEN`

## Remote state

- active backend: platform HTTP state service;
- state payload: AES-GCM client encrypted before upload;
- blob storage: private, versioned Timeweb S3;
- ownership and lock metadata: managed PostgreSQL;
- concurrent HTTP lock probe: **1 winner / 15 conflicts — PASS**;
- namespace isolation: admin credential received `403` for non-admin state;
- final admin OpenTofu plan: **No changes**.

## Admin system pool

- existing CI worker retained and labelled `pool=ci`;
- three 4 vCPU / 8 GiB system workers added and Ready;
- all system workers carry `ai-native-paas.io/system=true:NoSchedule`;
- the state service is scheduled on the system pool;
- managed PostgreSQL and the original cluster were adopted without destructive
  replacement or credential rotation.

## Timeweb network-drive CSI

- managed `csi-driver` add-on v2.0.0 installed;
- controller: **6/6 Ready** on the system pool;
- node DaemonSet: **4/4 Ready**;
- `CSIDriver network-drives.csi.timeweb.cloud`: present;
- Moscow NVMe StorageClass: present;
- live 10 GiB PVC write/delete/recreate/read sentinel: **PASS**;
- namespace cleanup and zero leaked PVs: **PASS**.

During initial inspection, a computed Helm-values command exposed the add-on's
issued API secret in operator output. The affected add-on was deleted immediately,
which revoked that credential, and a fresh add-on was installed before any PVC or
workload used it. The replacement credential was never printed or committed.
