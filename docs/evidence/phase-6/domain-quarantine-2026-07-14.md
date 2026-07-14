# Domain quarantine evidence — 2026-07-14

Evidence class: `LOCAL_GREEN`

Requirement: `A5.32` — `TestDomain_ReleaseEntersQuarantineBeforeAnotherTenantCanClaim`.

The executable pivot test proves that:

- releasing a claimed hostname moves it to `QUARANTINED` until an exact fake-clock deadline;
- another tenant cannot claim the hostname during that interval;
- the release transition is rejected one nanosecond before the deadline and accepted at the deadline;
- a later tenant receives a fresh ownership challenge and the hostname remains unroutable until verification.

Verification commands:

```text
go test ./test/pivot -run '^TestDomain_ReleaseEntersQuarantineBeforeAnotherTenantCanClaim$' -count=1
go test -race ./test/pivot -run '^TestDomain_ReleaseEntersQuarantineBeforeAnotherTenantCanClaim$' -count=1
go test ./...
go vet ./...
python3 scripts/generate-pivot-tdd-matrix.py --check
```

This is deterministic local evidence only. It does not claim the Timeweb DNS or ACME live gates (`A5.30`, `A5.31`).
