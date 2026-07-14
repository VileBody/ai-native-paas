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
- Every patch creates an immutable content-hashed source change plan.
  Production GitOps paths wait for a human grant bound to plan hash,
  workspace, branch and agent. The grant is consumed once when commit is
  authorized; identical lost-response replay is idempotent.
- The special commit and push paths recompute the actual Git tree change set
  and require it to equal the authorized plan hash.

Evidence:

- `go test ./...` passed after the source slice.
- Real local Git gates pass for exact checkout after mutable branch movement,
  concurrent expected-base push conflict, submodule policy, credential
  non-persistence, secret sentinel blocking and signed attestation.
- Managed PostgreSQL migrations `004_commit_receipts.sql` and
  `005_source_change_governance.sql` were applied through a
  temporary in-cluster test pod and
  `TestPostgres_WorkspaceCommitReceiptMigrationAndRoundTrip` passed. The pod
  was deleted immediately after the test.
- Pivot matrix discovers executable evidence for S2, S3, S4, S5, S6, S7,
  S14 and S17.

Remaining source gates:

- GitLab.com live token isolation, rename/transfer, archive and 429 tests;
- merge request creation/comment adapter and branch cleanup intent;
- command-runner UID separation so task processes cannot read the workspace
  agent's long-lived-on-disk mTLS private key.
