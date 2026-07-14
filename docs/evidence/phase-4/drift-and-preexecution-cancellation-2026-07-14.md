# Drift reconciliation and pre-execution cancellation — 2026-07-14

Evidence class: `LOCAL_GREEN`.

Requirements:

- `C17` — `TestUsage_ReconcilerCorrectsObservedResourceDriftOnce`;
- `C18` — `TestCommercial_CanceledBeforeExecutionCreatesNoUsageCharge`.

The usage reconciler compares the observed resource quantity with the immutable
ledger total for the same tenant, period, resource, meter and time window. A
positive drift appends one correction entry and an outbox record. Replaying the
same observation sees the corrected total, emits neither another correction nor
another outbox record, and does not duplicate the drift alert.

The commerce build-usage boundary treats `canceled` with equal start and finish
timestamps as cancellation before execution. Even if an untrusted report carries
non-zero CPU, memory and Docker VM counters, it creates no usage event. Replaying
the report remains free, and the invoice preview contains no lines and a zero
total.

These tests prove the local commerce ledger behavior. Stopping a running
workspace, revoking credentials and suppressing provider usage before dispatch
remain separate workspace/provider system gates.

Verification:

```text
go test -count=1 ./internal/commerce/application \
  -run 'TestUsage_ReconcilerCorrectsObservedResourceDriftOnce|TestCommercial_CanceledBeforeExecutionCreatesNoUsageCharge'
go test -count=1 ./...
go test -race -count=1 ./...
go vet ./...
make fmt-check generate-check
```

Matrix result:

- 157 requirements;
- 997 discovered Go test/fuzz targets;
- `REUSED 108`, `NEW 0`, `LIVE_ONLY 49`;
- zero unmapped requirements.
