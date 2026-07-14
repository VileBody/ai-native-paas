# Phase 2 evidence — sealed OpenBao HA release

Date: 2026-07-14 (Europe/Moscow)

## Deployed boundary

- Timeweb managed Kubernetes admin cluster `ai-native-paas-test`.
- Namespace `openbao` with Restricted Pod Security enforcement.
- Official OpenBao Helm chart `0.28.4`; downloaded chart SHA-256
  `783177e7925cb0d91ec3d5317a80826cb1e265ce17ba176d7a7ad578234c8a4e`.
- OpenBao `2.5.5` and injector `1.7.2`, both referenced by immutable
  multi-architecture manifest digests.
- Three HA servers using Integrated Storage (Raft), scheduled one per system
  worker through required pod anti-affinity.
- Two injector replicas with `failurePolicy: Fail` and a populated webhook CA
  bundle.
- TLS enabled for API and cluster transport; the private CA and key remain in
  the ignored operator custody directory, not in Git or OpenTofu state. The
  server TLS key exists only in the Kubernetes TLS secret and operator custody
  directory.
- Three 20 GiB data PVCs and three 10 GiB audit PVCs, all Bound with the
  Timeweb NVMe network-drive StorageClass and retained on release deletion or
  scale-down.

## Live observations

All three server pods were Running on different nodes and returned:

```json
{"initialized":false,"sealed":true,"storage_type":"raft","ha_enabled":true}
```

The TLS client used the generated CA and each pod's internal DNS name, proving
certificate verification rather than a plaintext or insecure-skip path.

The first server-pod attempt was rejected by Restricted Pod Security because
the chart did not explicitly drop Linux capabilities. The values now set
`allowPrivilegeEscalation: false`, RuntimeDefault seccomp and
`capabilities.drop: [ALL]`; the corrected pods passed admission. Helm 4's
server-side apply also conflicted with the injector-managed webhook CA field;
the deployment script now uses client-side apply, preserving the controller's
CA ownership and a clean deployed Helm revision.

## Intentionally incomplete

This is not an OpenBao provider-green claim. No Shamir keys or root token have
been created. Initialization, 5/3 independent holder custody, unseal, audit
enablement, Kubernetes auth, least-privilege policies, Raft membership,
snapshot upload/restore and leader failover remain gated by the ceremony in
`docs/runbooks/openbao-shamir-ceremony.md`.

No unseal share or root token was printed, stored in Kubernetes, committed to
Git, or written to CI evidence during this deployment.
