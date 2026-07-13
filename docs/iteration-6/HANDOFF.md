# Iteration 6 — handoff в Iteration 7

Agent Governance может использовать только frozen public contracts:

```go
type EntitlementService interface {
    Check(context.Context, EntitlementRequest) (EntitlementDecision, error)
    Reserve(context.Context, QuotaRequest) (QuotaReservation, error)
    Commit(context.Context, string) error
    Release(context.Context, string) error
}

type UsageSink interface {
    Append(context.Context, UsageEvent) error
}
```

## Agent workflow

```text
agent requests paid operation
        ↓
Agent Governance validates scope/approval/budget
        ↓
Commerce entitlement check
        ↓
quota reservation
        ↓
domain operation
        ↓
commit or release
        ↓
usage emitted by authoritative producer
```

## Запреты для Iteration 7

Agent layer не может:

- изменять plan version;
- напрямую вставлять rated usage/invoice lines;
- обходить reservation;
- подменять tenant/resource ownership;
- читать или редактировать commerce tables;
- превращать suspension в data deletion;
- выдавать себе approval на увеличение paid limits.

## Frozen outputs

- `EntitlementRequest/Decision`;
- `QuotaRequest/Reservation`;
- `UsageEvent` и meter catalog v1;
- `InvoicePreview`;
- `CommercialState` и `RuntimeIntent`.

Breaking change требует `commerce/v2`; исправления persistence остаются follow-up Iteration 6.
