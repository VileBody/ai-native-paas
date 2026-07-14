# Workspace command log S3 gate — 2026-07-14

Evidence tier: `PROVIDER_GREEN`

- Timeweb project: `BALOVSTVO`, Moscow admin infrastructure boundary;
- resource: dedicated private `twc_s3_bucket.workspace_logs`;
- exact OpenTofu plan: one create, zero updates, zero deletes;
- apply: success;
- post-apply OpenTofu drift: zero;
- S3 object versioning: enabled and read back as `Enabled` through the Timeweb
  S3 API;
- lifecycle: `prevent_destroy = true`;
- credentials: sensitive OpenTofu outputs only; no credential value is committed
  or written to evidence.

The bucket stores only client-encrypted, already-redacted workspace stdout and
stderr objects. It is not reused for OpenTofu state, custom-image staging,
Harbor blobs or user resources.
