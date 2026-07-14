# GitLab merge request plan summaries — 2026-07-14

Status: `LOCAL_GREEN`; GitLab adapter contract is green, while the
`PROVIDER_GREEN` GitLab.com live gate remains pending.

Implemented:

- Added GitLab Notes adapter support for
  `POST /projects/:id/merge_requests/:merge_request_iid/notes` and bounded note
  discovery through the corresponding list endpoint. This follows the
  official [GitLab Notes API](https://docs.gitlab.com/api/notes/).
- Plan summary publication requires a ready tenant-owned repository, an open
  persisted merge request, a project-matching immutable plan, and a matching,
  unexpired estimate version.
- The GitLab note contains only action counts, customer minimum/maximum in
  minor units, approval status, plan hash, estimate version, and immutable
  estimate expiry time.
- Resource addresses, provider identifiers, external IDs, raw OpenTofu values,
  provider costs, estimate meter names, credentials, and command output are not
  rendered.
- A content-derived HTML marker supports exact lost-response discovery. A note
  with the same marker but different content is rejected rather than adopted.
- Publication is idempotent through the Source inbox record and produces a
  value-free `source.merge_request_plan_summary_published.v2` outbox event and
  audit record.

Executable evidence:

- `TestSource_MergeRequestPublishesPlanSummaryWithoutSecrets`
- `TestMergeRequestPlanSummary_LostResponseRecoversExactSanitizedNote`
- `TestGitLab_CreateAndFindMergeRequestNoteUsesNotesAPI`
- `go test ./...`
- `go vet ./...`
- `go test -race ./internal/source/... ./test/pivot ./test/architecture`
- pivot matrix regeneration and `--check`

Matrix effect:

- S12 is now `REUSED` executable GitLab contract evidence.
- The complete matrix remains mapped at 157 requirements.
- Discovered Go test/fuzz targets: 893.
- Statuses: `REUSED 42`, `NEW 44`, `LIVE_ONLY 71`.

Not claimed:

- No live comment was posted to GitLab.com in this slice.
- GitLab.com project-token isolation, rename/transfer, archive and 429 gates
  remain pending.
