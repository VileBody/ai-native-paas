# OpenTofu state backends

The committed files contain no credentials. The active backend is HTTP; no
script may materialize the account-wide Timeweb S3 main-user key into
`.state-backend/`, Terraform output or a shell environment. The ignored
directory still holds the encrypted HTTP backend configuration and must remain
mode `0700`.

The Timeweb live probe on 2026-07-14 proved that object versioning works but
conditional `If-None-Match: *` writes do not: all sixteen contenders succeeded.
Therefore active stacks **must not** use the committed S3 backend examples.
They are retained as portable configuration and as a future re-test target.

Use `scripts/configure-http-state-backend.py`; the platform HTTP state service
owns PostgreSQL advisory/row locks and stores client-encrypted blobs in the
versioned S3 bucket. Backend credentials are supplied only through
`TF_HTTP_USERNAME` and `TF_HTTP_PASSWORD`.

The one-time S3 bucket bootstrap state is migrated into its own
`state-bootstrap` HTTP namespace after the service becomes available. The
state service receives a dedicated S3 user with rights only on the state
bucket; see `docs/runbooks/timeweb-s3-main-credential-rotation.md`.

`TF_VAR_state_passphrase` is the client-side encryption recovery key. Keep one
copy outside Timeweb and outside the Git repository. Losing it makes encrypted
state unrecoverable.
