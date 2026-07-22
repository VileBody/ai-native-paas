# Timeweb S3 main-user rotation

## Why this is a release blocker

Timeweb documents its automatically created S3 main user as account-wide: it
can access every bucket. A `twc_s3_bucket` resource may expose that same pair
for multiple buckets, so separate buckets alone do not isolate credentials.
The main user must not be used by a runtime consumer or stored in an OpenTofu
output. This runbook replaces it with least-privilege S3 users before the
administrator secret is reset. Timeweb retains the mandatory S3 administrator
principal; the goal is to invalidate its old secret and remove it from every
runtime consumer, not to delete the principal.

## Preconditions

- `state-service` is healthy and there is no active OpenTofu apply.
- Preserve the existing encrypted state and its offline recovery passphrase;
  this procedure rotates S3 transport credentials, not state encryption.
- Create a `0700` directory outside the repository. Each key file must be
  `0600`; never paste a key into a terminal command, chat, Git or a manifest.

```bash
install -d -m 700 "$HOME/.config/ai-native-paas/timeweb-s3"
```

## Create four Timeweb users

In the Timeweb Cloud S3 panel, open **Users → Add user**. Create one user per
row below and grant **Manage** only on the named bucket; grant **No access** to
every other bucket. `Manage` is currently the narrowest Timeweb level that
allows the required object read/write/delete lifecycle.

| User | Sole bucket | Consumer |
|---|---|---|
| `ai-native-paas-state-service` | platform state bucket | `state-service` |
| `ai-native-paas-workspace-logs` | `ai-native-paas-workspace-logs` | workspace manager |
| `ai-native-paas-image-staging` | `ai-native-paas-images` | custom-image and Talos artifact tools |
| `ai-native-paas-harbor` | `ai-native-paas-harbor-blobs` | Harbor registry |

Save each generated access key and secret key as its own private file. The
browser page itself is an authorized secret surface; do not copy its values to
the terminal history.

## Switch and prove every consumer

Switch state first, then prove an actual encrypted-state read through the HTTP
backend. A rollout being Ready is not proof that S3 authorization works.

```bash
export STATE_S3_ACCESS_KEY_FILE="$HOME/.config/ai-native-paas/timeweb-s3/state.access-key"
export STATE_S3_SECRET_KEY_FILE="$HOME/.config/ai-native-paas/timeweb-s3/state.secret-key"
./scripts/sync-state-service-s3-credentials.sh

export TF_HTTP_USERNAME=admin-migration
export TF_HTTP_PASSWORD="$(<.state-backend/http-password)"
./scripts/verify-state-service-s3-read.sh
```

Then switch the remaining consumers. The workspace-manager Secret may be
prepared while that workload is intentionally not deployed.

```bash
export WORKSPACE_LOG_S3_ACCESS_KEY_FILE="$HOME/.config/ai-native-paas/timeweb-s3/workspace-logs.access-key"
export WORKSPACE_LOG_S3_SECRET_KEY_FILE="$HOME/.config/ai-native-paas/timeweb-s3/workspace-logs.secret-key"
./scripts/sync-workspace-manager-secrets.sh

export HARBOR_BLOB_S3_ACCESS_KEY_FILE="$HOME/.config/ai-native-paas/timeweb-s3/harbor.access-key"
export HARBOR_BLOB_S3_SECRET_KEY_FILE="$HOME/.config/ai-native-paas/timeweb-s3/harbor.secret-key"
./scripts/deploy-harbor.sh
./scripts/harbor-live-oci-gate.sh
```

For image staging, keep the two `IMAGE_STAGING_S3_*_FILE` variables available
only to the operator command that imports an image or moves a Talos bootstrap
artifact. Run its existing digest/round-trip gate before treating it as
switched.

## Reset and verify the administrator secret

Only after every applicable consumer is proved, open the S3 administrator in
**S3 → Users** and reset its secret. Do not try to delete the mandatory
administrator. The legacy public API route may return `404` for migrated
accounts, in which case the authenticated account panel is the authoritative
rotation surface.

Run a harmless authenticated request with the old credential against the state
bucket and require rejection; discard the response body and never print the old
pair. Also require the rotated administrator credential and every scoped
consumer to remain accepted. Record only `old-key rejected` and the timestamp
in release evidence. Remove stale local `credentials.env` files once no
operator uses them; a `.s3.tfbackend` file is not a credential by itself and
may remain as offline recovery configuration when its access/secret values are
provided separately.

If a new user fails before the administrator reset, restore the previous
working consumer and fix the new user's bucket scope. After reset, use the
rotated administrator only for operator recovery and never install it into a
runtime Secret. Do not widen a dedicated user's rights beyond its single bucket
as a shortcut.
