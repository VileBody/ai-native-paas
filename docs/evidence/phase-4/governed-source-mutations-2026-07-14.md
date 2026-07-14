# Governed source mutation slice — 2026-07-14

Status: `LOCAL_GREEN`, `DB_GREEN`; provider-live GitLab gate remains pending.

Implemented:

- Project MCP repository tools resolve repository, exact source revision,
  actor, task and correlation from verified scope rather than arguments.
- `repository_create_branch` performs an exact-SHA checkout inside the
  disposable workspace and leaves a credential-free HTTPS remote URL.
- Patch paths are canonicalized and reject `.git`, traversal, duplicates,
  symlink escapes and protected production GitOps paths before mutation.
- Governed commits scan changed regular files for credential rules, reject
  content filters/submodules, add correlation trailers, and produce an ECDSA
  attestation signed by the workspace's short-lived mTLS identity.
- The workspace-manager verifies the detached signature against the same
  client certificate used by the outbound agent session and persists the
  command-scoped receipt in PostgreSQL.
- Pushes verify the repository URL, exact parent revision, commit trailers,
  protected paths and secret scan again, then use only `--force-with-lease`.
- Production GitOps paths fail closed with `APPROVAL_REQUIRED`; a caller
  supplied approval identifier is not trusted until the source approval
  service is implemented.

Evidence:

- `go test ./...` passed after the source slice.
- Real local Git gates pass for exact checkout after mutable branch movement,
  concurrent expected-base push conflict, submodule policy, credential
  non-persistence, secret sentinel blocking and signed attestation.
- Managed PostgreSQL migration `004_commit_receipts.sql` was applied through a
  temporary in-cluster test pod and
  `TestPostgres_WorkspaceCommitReceiptMigrationAndRoundTrip` passed. The pod
  was deleted immediately after the test.
- Pivot matrix now discovers 869 Go test/fuzz functions; S2, S3, S5, S6, S7,
  S14 and S17 are executable `REUSED` evidence.

Remaining source gates:

- exact source-plan approval storage and human grant path for production;
- GitLab.com live token isolation, rename/transfer, archive and 429 tests;
- merge request creation/comment adapter and branch cleanup intent;
- command-runner UID separation so task processes cannot read the workspace
  agent's long-lived-on-disk mTLS private key.
