# Iteration 6 — threat model

## Защищаемые активы

- тарифы и immutable plan versions;
- quota capacity;
- usage ledger;
- invoice previews;
- коммерческое состояние tenant;
- outbox/audit;
- tenant/resource attribution.

## Основные угрозы

### Double charge через duplicate telemetry

Защита: tenant-scoped idempotency key + normalized payload fingerprint + unique database constraint. Повтор идентичного события — no-op; изменённый payload — conflict.

### Underbilling через микроскопические события

Защита: micro-usage агрегируется до округления; integer/rational rating. Нельзя округлять каждое событие к нулю.

### Overflow / wraparound

Защита: checked arithmetic и `math/big` для pricing; overflow возвращает domain error. SQL monetary fields — `BIGINT`/`NUMERIC`, не floating point.

### Quota race

Защита: Serializable transactions, unique idempotency, optimistic versions и повтор транзакции. Concurrent reservations не могут превысить limit.

### Cross-tenant usage injection

Защита: tenant определяется auth/path boundary; `ResourceOwnership` подтверждает resource; отсутствующий resolver fail-closed; SQL uniqueness tenant-scoped.

### Price rewriting after consumption

Защита: active plan immutable, billing period pin-ит `plan_version_id`, database triggers запрещают изменение identity/spec.

### Ledger tampering

Защита: usage, idempotency, audit и alerts append-only на уровне БД. Corrections являются новыми records.

### Suspension as destructive action

Защита: runtime intent явно содержит `retain_managed_services=true`; Commerce не имеет provider credentials и не вызывает purge.

### Unauthorized platform-admin mutations

Защита: отдельный admin route boundary и role check. Production deployment должен заменить trusted headers на verified OIDC/service identity.

### API cache/data leakage

Защита: `Cache-Control: no-store`, stable error envelope, unknown-field rejection и отсутствие internal causes в ответе.

## Остающиеся production risks

- cryptographically verified identity вместо development headers;
- rate limiting и abuse controls;
- signed producer usage envelopes;
- durable message broker delivery/replay;
- tax/discount authorization;
- tamper-evident external audit storage;
- PostgreSQL HA/failover and restore validation;
- full Attachments → Commerce integration after source restoration.
