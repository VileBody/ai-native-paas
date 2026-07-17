# Internal Harbor development profile

The `ai-native-paas-harbor` release is an admin-plane registry. It is always
scheduled on the tainted system node pool and is exposed only as the
TLS-protected `ClusterIP` service `harbor.harbor-system.svc`.

The development profile intentionally has one replica, internal Redis, no
Trivy and no public ingress. It is not a beta/HA deployment. Its immutable
chart package is pinned by `scripts/deploy-harbor.sh`; component image-digest
locking, Trivy, production robot-lease issuance and the disposable-workspace
receipt gate remain separate release work.

Harbor metadata uses the dedicated `harbor` database and `harbor_admin` user
in the admin managed PostgreSQL cluster. OCI blobs use the dedicated private
Timeweb S3 bucket. Neither resource is shared with OpenTofu state, workspace
logs, image staging or any tenant resource.

The database bootstrap credential is derived from encrypted OpenTofu state.
The S3 access and secret keys are instead supplied as paths to private files
for a dedicated Timeweb S3 user that can manage only the Harbor bucket. The
sync command never prints values and preserves generated chart secrets on
rerun:

```bash
export HARBOR_BLOB_S3_ACCESS_KEY_FILE="$HOME/.config/ai-native-paas/timeweb-s3/harbor.access-key"
export HARBOR_BLOB_S3_SECRET_KEY_FILE="$HOME/.config/ai-native-paas/timeweb-s3/harbor.secret-key"
./scripts/deploy-harbor.sh
```

Both commands require the standard encrypted-admin-state environment:
`TF_VAR_state_passphrase`, `TF_HTTP_USERNAME` and `TF_HTTP_PASSWORD`. Use
`admin_capacity_mode=dev` while in the solo development window; never let a
default plan silently scale system workers to HA.

`deploy-harbor.sh` restarts only the registry after the Secret sync, because
Kubernetes does not restart a Secret consumer automatically.

To run the live internal registry gate (private project, one project-scoped
robot, OCI blob/manifest S3 round trip, actual post-revoke read denial, then
cleanup), use only Kubernetes access:

```bash
./scripts/harbor-live-oci-gate.sh
```

The gate creates no public endpoint and leaves no project, robot, Job or Pod
on success. It is storage/credential evidence only; it does not mark any build
artifact releasable.
