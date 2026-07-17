# Remote state service

The service implements the OpenTofu HTTP backend protocol. State payloads must
already be encrypted by OpenTofu; plaintext state is rejected. Blobs are stored
in the versioned Timeweb S3 bucket while lock and ownership metadata is stored
in admin PostgreSQL.

The beta bootstrap manifest exposes only a ClusterIP and uses a small set of
expiring, namespace-scoped migration credentials. Operators access it through a local
`kubectl port-forward`; no public unauthenticated endpoint is created. The
credential secret is generated out-of-band and is never committed.

Rotate the four short-lived namespace credentials before an IaC session; the
script updates the ignored local credential files and restarts the service
without printing any password:

```bash
./scripts/rotate-state-service-credentials.sh
```

The Timeweb main S3 credential is account-wide and is not a service
credential. Give the state bucket its own Timeweb S3 user, store its access
and secret keys in private `0600` files outside the repository, then switch
the deployment and verify a real encrypted-state read:

```bash
export STATE_S3_ACCESS_KEY_FILE="$HOME/.config/ai-native-paas/timeweb-s3/state.access-key"
export STATE_S3_SECRET_KEY_FILE="$HOME/.config/ai-native-paas/timeweb-s3/state.secret-key"
./scripts/sync-state-service-s3-credentials.sh

export TF_HTTP_USERNAME=admin-migration
export TF_HTTP_PASSWORD="$(<.state-backend/http-password)"
./scripts/verify-state-service-s3-read.sh
```

Do not block the Timeweb main user until this read succeeds and the remaining
workspace-log, image-staging and Harbor consumers have been switched.
