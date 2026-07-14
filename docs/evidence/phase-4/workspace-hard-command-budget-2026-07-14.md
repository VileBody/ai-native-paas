# Workspace hard command budget evidence — 2026-07-14

Evidence class: `LOCAL_GREEN`

Requirement: `C12` —
`TestBudget_WorkspaceCommandStoppedBeforeExceedingHardLimit`.

Workspace dispatch now reserves and commits the command's complete timeout in
Commerce before a command can reach an agent session. The reservation is
tenant-bound, idempotent by project/task/command, and must grant exactly the
requested number of seconds. A partial, expired, oversized, or workspace-TTL
exceeding lease is rejected.

Observed invariants:

- a project with only 10 command-seconds remaining rejects a 20-second command
  before the session dispatcher is called;
- a 10-second command receives an immutable reservation identity and absolute
  deadline;
- the session registry rejects missing, expired, or enlarged leases and
  carries the accepted lease to the workspace agent;
- the workspace agent binds the executor context to the Commerce deadline and
  reports `TIMED_OUT` with process-tree termination evidence;
- the production client requires HTTPS plus an injected mTLS client and has no
  bearer-token or trusted-header fallback;
- the Commerce service scope `commerce.workspace_budget:write` can cross the
  platform tenant boundary only for quota reserve/commit routes, not for other
  Commerce operations.

Verification commands:

```text
go test ./...
go vet ./...
go test -race ./internal/workspaceagent ./internal/workspace/commercebudget \
  ./internal/commerce/httpapi ./internal/workspace ./internal/workspace/session \
  ./test/pivot
./scripts/generate-pivot-tdd-matrix.py --check
```

The Commerce command-seconds reservation is an autonomous execution allowance,
not a billable usage event. This evidence does not claim a live Commerce mTLS
exchange, a Timeweb workspace VM, or provider-denominated usage accounting;
those remain separate `SECURITY_GREEN` and provider gates.
