# Autonomous repair budget stop evidence — 2026-07-14

Evidence class: `LOCAL_GREEN`

Requirements:

- `C13` — `TestBudget_RepairLoopConsumesConfiguredNotUnlimitedBudget`;
- `G25` — `TestAgent_RepairLoopAndBudgetStopAutonomousSpend`.

Agent governance reserves build/deploy budget in the same store transaction
that creates the invocation intent. When the next action would exceed the
configured budget, that transaction persists `PAUSED` without creating an
invocation or calling a downstream provider.

Observed invariants:

- deterministic repair failures consume the configured build count and build
  minutes rather than receiving an unlimited retry allowance;
- reaching the repair threshold pauses the task and blocks the next provider
  call;
- a human resume clears the repair fingerprint but does not reset consumed
  build/deploy budget;
- an action attempted after human resume with exhausted numeric budget pauses
  the task again before any external side effect;
- unrelated mutating tools are denied while the task is paused.

Verification commands:

```text
go test -count=1 ./internal/agent/application \
  -run '^(TestBudget_RepairLoopConsumesConfiguredNotUnlimitedBudget|TestAgent_RepairLoopAndBudgetStopAutonomousSpend)$'
go test -race -count=1 ./internal/agent/... ./pkg/contracts/agent/v1
```

This is deterministic Agent governance evidence. It does not claim a live
provider failure loop or a provider-denominated cost ledger gate.
