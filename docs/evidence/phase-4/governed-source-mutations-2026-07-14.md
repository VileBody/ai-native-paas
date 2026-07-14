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
- `repository_create_merge_request` accepts no provider project ID, target
  branch or commit SHA from the agent. It binds the tenant-scoped numeric
  GitLab identity and default branch to the authorized source plan and exact
  signed commit receipt, then verifies that the remote source branch still
  points to the attested SHA before creating the MR.
- Merge request descriptions are generated only from bounded plan hash, SHA,
  task, correlation and actor metadata. Lost-response and concurrent retries
  recover only a provider MR with that exact description and source/target
  pair, so a pre-existing human MR cannot be mistaken for platform evidence.
- Branch reconciliation publishes canonical `source.revision_observed.v2`
  events exactly once for missed provider changes. Out-of-order webhook
  deliveries are retained as stale receipts without regressing the head.
- Preview branch/environment bindings and deletion metadata are durable;
  deletion publishes a cleanup intent instead of mutating runtime directly.
- Merge request plan summaries are published through the GitLab Notes API from
  action counts and customer cost ranges only. Exact marker recovery handles a
  lost response without exposing resource/provider values.
- GitLab discovery calls have bounded `429` retry using documented reset
  headers. Non-idempotent project creation is never blindly retried; recovery
  uses the exact correlation marker.

Evidence:

- `go test ./...` passed after the source slice.
- `go vet ./internal/source/... ./internal/project/mcp/... ./cmd/project-api/...`
  and race tests for source application, GitLab adapter and Project MCP passed.
- Real local Git gates pass for exact checkout after mutable branch movement,
  concurrent expected-base push conflict, submodule policy, credential
  non-persistence, secret sentinel blocking and signed attestation.
- Managed PostgreSQL migrations `004_commit_receipts.sql` and
  `005_source_change_governance.sql` were applied through a
  temporary in-cluster test pod and
  `TestPostgres_WorkspaceCommitReceiptMigrationAndRoundTrip` passed. The pod
  was deleted immediately after the test.
- Pivot matrix discovers executable evidence for S2, S3, S4, S5, S6, S7,
  S9, S10, S12, S13, S14, S15 and S17; the complete matrix remains mapped at
  157 requirements and now discovers 896 Go test/fuzz targets.
- Managed PostgreSQL migration `004_branch_environment_cleanup.sql`, the full
  tagged Source suite and the real Store round-trip passed in temporary
  in-cluster test pods; see
  `docs/evidence/phase-4/source-reconciliation-and-cleanup-2026-07-14.md`.
- The GitLab Notes adapter, sanitized renderer and lost-response recovery pass
  the S12 contract; see
  `docs/evidence/phase-4/gitlab-plan-summary-notes-2026-07-14.md`.
- Bounded rate-limit discovery recovery passes the S15 contract; see
  `docs/evidence/phase-4/gitlab-rate-limit-recovery-2026-07-14.md`.

Remaining source gates:

- GitLab.com live token isolation, rename/transfer and archive tests;
- runtime consumption of preview cleanup intents;
- live signed-image proof for the implemented task-UID and command-scoped
  identity-FD boundary; see
  `docs/evidence/phase-3/workspace-command-identity-separation-2026-07-14.md`.
