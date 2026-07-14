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
