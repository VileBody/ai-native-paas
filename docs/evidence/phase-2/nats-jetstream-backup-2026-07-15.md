# NATS JetStream encrypted backup — 2026-07-15

Target: `ai-native-paas-test`, namespace `nats`.

The live `nats account backup --check` operation completed for five streams:

- `PLATFORM_BOOTSTRAP`;
- `PLATFORM_EVENTS`;
- `PLATFORM_OPERATIONS`;
- `PLATFORM_USAGE`;
- `WORKSPACE_COMMANDS`.

The backup reported five healthy streams, 392 bytes of stored messages and no
consumers. The plaintext backup directory was archived, encrypted locally with
AES-256-CBC/PBKDF2 (200,000 iterations), and uploaded to the versioned private
Timeweb bucket at:

```text
s3://ai-native-paas-state/admin-backups/nats/20260715T205041Z/nats-account.tar.gz.enc
```

Evidence identity:

```text
S3 version: mFN594tEYz.uChGwsy7B0SDto27r2yv
SHA-256: cf04978d71d19fbbfa0369be3ee54a4853a99004351d60554677e2396f220f00
```

Verification downloaded the encrypted object from S3, matched its SHA-256,
decrypted it with the ignored operator recovery material, and successfully
listed the resulting tar archive. Plaintext local copies were then removed.
The encrypted local copy and recovery material remain under the ignored
`.operator/admin-backups/` custody directory with owner-only permissions.

This evidence satisfies the backup/export prerequisite for admin `dev` or
`off`. It does not replace a later live restore drill before beta release.
