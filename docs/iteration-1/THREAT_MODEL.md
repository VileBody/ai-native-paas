# Итерация 1 — threat model

## Защищаемые активы

- tenant membership и role assignments;
- identity-to-principal mapping;
- operation state и result;
- idempotency records;
- domain events;
- audit history;
- correlation/causation chain;
- provider/internal errors и credentials;
- PostgreSQL integrity.

## Trust boundaries

```text
Untrusted HTTP caller
        ↓
Authentication boundary
        ↓
PrincipalContext without tenant claim
        ↓
Application authorization + idempotency
        ↓
Domain aggregates
        ↓
Store transaction
        ↓
PostgreSQL / outbox publisher
```

OIDC mapper принимает только claims, уже проверенные cryptographic verifier. Signature/JWKS verification не относится к mapper и пока не подключено к executable.

## Основные угрозы и контроли

### Cross-tenant access

Угроза: caller подставляет tenant ID другого клиента или ложный tenant claim.

Контроли:

- request header tenant context игнорируется;
- OIDC tenant-like claims игнорируются;
- resource tenant выводится server-side;
- membership должна существовать и быть `ACTIVE`;
- principal tenant и resource tenant обязаны совпадать;
- negative domain, service и HTTP acceptance tests.

### Confused deputy / scope escalation

Угроза: валидный principal использует широкую membership не имея нужного OAuth scope, либо наоборот.

Контроли:

- authorization требует одновременно scope и role capability;
- admin не управляет owner memberships;
- invitation не даёт permissions;
- suspended/removed membership не даёт permissions.

### Duplicate commands и replay

Угроза: retry, concurrent requests или malicious replay создают несколько организаций/операций.

Контроли:

- idempotency scope включает tenant/platform, principal и command;
- payload canonical fingerprint;
- unique `(scope, idempotency_key)`;
- original response хранится атомарно;
- mismatch возвращает stable conflict;
- 64-worker concurrency tests и race detector.

### Lost update

Угроза: два writer перезаписывают membership или operation state.

Контроли:

- aggregate version;
- compare-and-swap update;
- stable optimistic-lock error;
- in-memory и live-PostgreSQL integration tests.

### Partial transaction

Угроза: aggregate сохранён без event/audit или наоборот.

Контроли:

- domain mutation, outbox, audit и idempotency completion используют одну transaction;
- rollback tests;
- live-PostgreSQL atomicity test prepared.

### Event duplication и corruption

Угроза: broker retry доставляет event повторно либо один event ID используется с другим payload.

Контроли:

- outbox at-least-once explicitly modelled;
- leases permit crash recovery;
- inbox canonical fingerprint;
- duplicate same payload ignored;
- different payload rejected;
- concurrent inbox test.

### Audit tampering

Угроза: application или DBA-level query изменяет историю.

Контроли:

- append-only repository port;
- no update/delete application methods;
- PostgreSQL UPDATE/DELETE triggers;
- immutable audit integration test prepared;
- audit events in outbox.

Ограничение: superuser/physical database administrator всё ещё может обойти logical controls. Для stronger evidence позже нужен external/WORM audit sink.

### Secret leakage

Угроза: credentials появляются в audit, events, public errors или panic response.

Контроли:

- recursive sensitive-key redaction;
- defensive event envelope redaction;
- internal error causes не сериализуются;
- panic middleware отдаёт generic error;
- dedicated redaction tests.

Ограничение: произвольное secret value под несекретным key автоматически распознать нельзя. Producers обязаны не помещать credentials в metadata/events.

### Denial of service

Угроза: oversized JSON, expensive request flood, unbounded audit/list queries.

Текущие контроли:

- 1 MiB body limit;
- strict single-object JSON;
- audit list cap в HTTP handler;
- context cancellation propagates to store.

Остаток: rate limits, per-tenant command quotas, connection limits и global load shedding относятся к platform edge и ещё не реализованы.

## Security assumptions

- TLS termination и authenticating reverse proxy находятся перед API;
- OIDC verifier проверяет signature, issuer, audience, expiry и nonce до mapper;
- PostgreSQL connection uses TLS или защищённую private network;
- event consumers выполняют side effects в inbox transaction либо имеют собственную idempotency;
- service account database role не является PostgreSQL superuser;
- migration role отделён от runtime role.

## Residual risks

Подробный actionable список находится в [`TODO.md`](./TODO.md). Критические остатки перед production: live PostgreSQL verification, production OIDC verifier, persistent executable wiring, rate limiting и external broker tests.
