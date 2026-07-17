# Legacy workload quarantine

This directory keeps the operator-owned `rec-sidecar` and `vietnam-rent`
applications separate from both the admin control plane and the Cozystack PaaS
runtime. See ADR 0006.

## Invariants

- `call-analytics` and `dental-practice-assistant` are not migrated.
- Git revisions and image digests are immutable.
- Secrets are copied API-to-API and never committed or written as plaintext
  migration files.
- The retained Timeweb IPv4 resources are checked before and after old-cluster
  deletion with `scripts/check-preserved-timeweb-ips.sh`.
- The SPB load balancer reaches the Moscow CI node through two DNAT rules on
  the existing admin router IPv4: `30870/tcp` and `30443/tcp`. The rules create
  no additional IPv4 allocation.
- Applying the `Application` objects is the cutover step. Do not apply them
  until the destination PVCs are restored and the source writers are stopped.

## Argo CD Core

Install the pinned headless controller:

```bash
KUBECONFIG=infra/timeweb/ai-native-paas-test.kubeconfig \
  ./scripts/install-legacy-argocd.sh
```

After restoring data and stopping source writers, apply
`argocd-project-applications.yaml`. Patch every Deployment to the digest in
`images.lock.json`; Argo ignores only the image and CI-node placement fields and
continues to reconcile the rest of each exact-SHA Git manifest.

Apply `rec-live-overlay.yaml` before pinning images. It preserves the two
live-only workloads absent from the pinned rec-sidecar Git revision. The old
wildcard nginx edge is intentionally omitted because the pinned Caddy edge
already owns all four public host routes.

The three Caddy-managed certificates are migrated API-to-API into the
`legacy-edge-caddy-tls` Secret. Apply `edge-caddy-override.yaml`, then patch
`clean-start-edge` with `edge-caddy-deployment-patch.yaml`. Argo ignores only
that Deployment's TLS mount and that ConfigMap's data; it still reconciles the
rest of the exact-SHA workload.

## Migration evidence

The completed migration, backups, public probes, and the Timeweb node-IP
preservation incident are recorded in
`docs/evidence/legacy-quarantine-migration-2026-07-15.md`. The exact-IP check is
deliberately still failing for the two node addresses removed by Timeweb during
cluster deletion; do not change its expected set without resolving the support
incident.
