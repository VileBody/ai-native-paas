# Iteration 6 — реализация Commercial Governance

## Назначение

Домен принимает коммерческие решения **до** дорогого или необратимого side effect и формирует воспроизводимый ledger после фактического потребления ресурсов.

```text
request resource
    ↓
entitlement + quota reservation
    ↓
external operation
    ↓
commit/release reservation
    ↓
usage ingestion
    ↓
rating
    ↓
invoice preview
```

## Владение

PostgreSQL schema: `commerce`.

Агрегаты и записи:

- `PlanDefinition`;
- immutable `PlanVersion` после activation;
- `Subscription`;
- `BillingPeriod`;
- `CommercialAccount`;
- `QuotaReservation`;
- append-only `UsageEvent`;
- `ReconciliationAlert`;
- command idempotency;
- transactional outbox;
- append-only audit.

Cross-domain foreign keys и SQL joins отсутствуют.

## Планы и цены

Тариф является версионированным объектом:

```text
PlanDefinition
├── PlanVersion 1
├── PlanVersion 2
└── PlanVersion 3
```

Billing period фиксирует exact `plan_version_id`. Изменение цены создаёт новую версию и не переоценивает уже открытый период.

В денежном контракте отсутствуют `float32`/`float64`. Цена задаётся как точная рациональная величина:

```go
type Price struct {
    MinorUnits  int64
    PerQuantity int64
}
```

Rating использует integer arithmetic и `math/big`; округление выполняется на определённой invoice-line boundary после агрегации micro-usage.

## Entitlements

Решение содержит не только boolean:

```text
allowed
reason
policy_version
plan_version_id
limit
remaining
```

Проверяются tenant identity, состояние subscription/account, trial expiry, feature, resource quota и billing period semantics.

## Quota reservation

Операция разбита на три стадии:

```text
RESERVED → COMMITTED
         ↘ RELEASED
```

Reservation:

- tenant-scoped;
- имеет TTL;
- идемпотентна по command key;
- учитывается при конкурентных allocation requests;
- освобождает capacity после release/expiry;
- не может быть committed после expiry.

## Usage ledger

Usage event содержит exact UTC window, resource identity, meter, kind и idempotency key.

Поведение повторов:

```text
same key + same normalized payload     → no-op
same key + different normalized payload → CONFLICT
```

Отрицательные quantities разрешены только для `CREDIT` и `CORRECTION`. Resource ownership проверяется через отдельный port и fail-closed при отсутствии resolver.

Meter catalog v1 включает runtime, build, storage, database, object storage, egress и logs.

## Runtime/build metering

Runtime quantity:

```text
interval seconds × replicas × unit weight
```

Scale events дают piecewise usage; suspension останавливает compute usage на границе; clock skew и overlapping observations не создают отрицательную или повторную величину.

Build policy различает:

- success;
- user-code failure;
- platform failure;
- canceled before start;
- отдельный Docker VM meter.

Platform failure не тарифицируется. Все meters одного build записываются атомарно.

## Rating и invoice preview

Preview детерминирован из immutable inputs:

1. выбрать usage текущего tenant/period;
2. сгруппировать по resource/meter/kind;
3. применить included allowance;
4. оценить overage по versioned price;
5. сохранить credit/correction отдельными lines;
6. нормализовать порядок;
7. вычислить total в minor units.

UTC устраняет зависимость от DST.

## Commercial lifecycle

```text
ACTIVE → GRACE → SUSPENDED → ACTIVE
```

Suspension выпускает runtime intent:

```text
stop compute
retain managed services
retain storage
retain source/artifacts
```

Commerce не удаляет PostgreSQL, Redis, S3, PVC или исходники.

## Reconciliation

Reconciler сравнивает observed allocation с ledger. Missing interval оформляется отдельным immutable correction event. Повтор observation не создаёт второй correction. Drift выше threshold создаёт append-only alert.

## Persistence

Три checksummed migrations:

- `001_commerce.sql` — schema/tables/indexes;
- `002_immutability.sql` — append-only и identity/state guards;
- `003_payload_consistency.sql` — согласование индексируемых колонок и JSONB aggregate.

Transactions используют Serializable isolation с bounded retry. Aggregate mutation, outbox и audit коммитятся атомарно.

## HTTP/API

`cmd/commerce-api` предоставляет development/process-test API:

- platform-admin plan/subscription operations;
- tenant entitlement checks;
- reserve/commit/release quota;
- append usage;
- invoice preview;
- grace/suspend/resume;
- reconciliation.

API использует explicit snake_case request DTO, unknown-field rejection, body limit, stable error envelope, tenant/path matching и `Cache-Control: no-store`.

По умолчанию resource ownership fail-closed. `COMMERCE_DEV_ALLOW_ALL_OWNERSHIP=true` существует только для локального smoke test и пишет warning.
