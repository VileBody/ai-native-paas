# Reservation commit before external apply — 2026-07-14

Evidence class: `LOCAL_GREEN`.

Requirement: `C5` —
`TestQuota_ReservationCommittedBeforeExternalApply`.

The production Project MCP apply path now has an explicit, tested ordering
invariant:

```text
validate exact plan + estimate + reservation + approval
→ durably authorize apply / consume one-time approval
→ dispatch verified-tofu-apply to the workspace
```

`Infrastructure.AuthorizeApply` is the persistence boundary. Its memory and
PostgreSQL stores set `ApplyStartedAt`, bind the apply idempotency fingerprint
and consume the approval grant before returning. The workspace dispatch is the
first external apply side effect and remains after that boundary.

The canonical MCP test observes the stored plan from the workspace adapter at
the exact moment dispatch begins. It proves that the matching execution
reservation is present, unexpired and already durably marked started. A second
plan is allowed to expire; its apply returns `POLICY_DENIED` and the workspace
external-call counter remains unchanged.

Invalid or expired authorization contracts are now consistently mapped to
`ErrPermissionDenied`, avoiding a misleading retryable `UNAVAILABLE` response
for a terminal reservation failure.

Verification:

```text
go test -count=1 ./internal/project/mcp \
  -run '^TestQuota_ReservationCommittedBeforeExternalApply$' -v
go test -count=1 ./...
go test -race -count=1 ./...
go vet ./...
make fmt-check generate-check
```

Matrix result:

- 157 requirements;
- 992 discovered Go test/fuzz targets;
- `REUSED 104`, `NEW 0`, `LIVE_ONLY 53`;
- zero unmapped requirements;
- C5 now maps to the executable Project MCP ordering gate.

This evidence covers the execution-reservation boundary. Partial provider
settlement and release of unused reserved value remain the separate C8 gate.
