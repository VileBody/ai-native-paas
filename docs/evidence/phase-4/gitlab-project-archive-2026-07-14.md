# GitLab project archive and purge boundary — 2026-07-14

Status: `LOCAL_GREEN`; GitLab adapter/provider contract is green, while the
live GitLab.com mutation gate remains pending.

Implemented:

- Archive closes the local source gate before calling GitLab, so new workspace
  creation and execution fail closed during an archive or lost response.
- GitLab archive and unarchive use the provider's idempotent project lifecycle
  endpoints and require the response to confirm the exact numeric project ID
  and requested archived state.
- A retry after a lost archive response resumes the same incomplete command,
  archives the same provider project and completes without duplicating local
  evidence.
- Archive revokes every distinct persisted workspace repository credential and
  emits tenant-scoped audit and outbox evidence.
- Restore preserves the provider project ID, path and repository metadata. Old
  workspace tokens remain revoked; new executions obtain fresh short-lived
  credentials.
- Purge is a separate destructive command that is allowed only from the
  archived state. It fails closed unless an approval verifier consumes a grant
  bound to tenant, project, repository, provider project, actor and
  idempotency key.
- GitLab `404` on repeated deletion is treated as idempotent success. Local
  state becomes `PURGE_PENDING`, reflecting GitLab.com's delayed-deletion
  semantics instead of claiming immediate physical deletion.

The provider behavior follows the official
[GitLab Projects API](https://docs.gitlab.com/api/projects/) archive,
unarchive and delete contracts.

Executable evidence:

- `TestSource_ProjectArchiveIsTwoPhaseAndReversibleBeforePurge`
- `TestGitLab_ArchiveRestoreAndDeleteUseProjectLifecycleAPI`
- `TestRepository_ArchiveRestoreAndPurgeStateMachine`
- `go test ./...`
- `go vet ./...`
- `go test -race ./internal/source/... ./test/pivot ./test/architecture`
- pivot matrix regeneration and `--check`

Matrix effect:

- S16 is now `REUSED` executable provider-contract evidence.
- The complete matrix remains mapped at 157 requirements.
- Discovered Go test/fuzz targets: 899.
- Statuses: `REUSED 44`, `NEW 44`, `LIVE_ONLY 69`.

Not claimed:

- No project was archived, restored or deleted in the live GitLab.com group.
- This slice governs the platform-managed source repository and its workspaces;
  broader runtime resource retention/purge orchestration remains a later
  project-lifecycle gate.
- Production purge remains intentionally unavailable until the central
  approval verifier is wired into the source entrypoint.
