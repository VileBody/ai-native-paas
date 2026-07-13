# Итерация 1 — Platform Kernel: реализация

## Статус

Функциональная часть итерации реализована. Все доступные в текущем окружении domain, application, contract, architecture, HTTP acceptance, race и fuzz-тесты проходят. PostgreSQL adapter и живой integration suite подготовлены, но сами PostgreSQL-тесты требуют внешнего сервера и поэтому в текущем окружении только компилируются и корректно помечаются как `SKIP`.

Оставшиеся ограничения перечислены отдельно в [`TODO.md`](./TODO.md).

## Что входит

```text
contracts/kernel/v1
    стабильные v1 value objects, errors, event/audit envelopes и ports

internal/kernel
    Organization / Membership / Operation
    RBAC и tenant isolation
    idempotent application commands
    transactional outbox / inbox
    append-only audit + redaction

internal/kernel/memory
    copy-on-write transactional adapter для deterministic tests и local dev

adapters/postgres/kernel
    database/sql adapter
    forward-only checksummed migrations
    optimistic locking
    outbox leasing через FOR UPDATE SKIP LOCKED
    database-enforced immutable audit

adapters/oidc
    mapping уже криптографически проверенных OIDC claims в PrincipalContext

internal/kernel/httpapi
    минимальный HTTP API и стабильные public errors

cmd/kernel-api
    local-development executable с in-memory storage
```

## Граница домена

Итерация владеет только PostgreSQL schema `kernel`:

```text
kernel.organizations
kernel.memberships
kernel.operations
kernel.idempotency_records
kernel.outbox_events
kernel.inbox_events
kernel.audit_records
kernel.schema_migrations
```

Production-код не содержит ссылок на будущие schemas `source`, `build`, `runtime`, `attachments`, `commerce` и `agent`. Architecture tests блокируют такие зависимости.

## Стабильные v1-контракты

Опубликованы в `contracts/kernel/v1`:

- `PrincipalContext`;
- `TenantRef`;
- `ResourceRef`;
- `OperationRef` и `OperationSnapshot`;
- `CommandMeta`;
- `PublicError`;
- `DomainEventEnvelope[T]`;
- `AuditEnvelope`;
- `Authorizer`;
- `OperationService`;
- `EventPublisher`.

Breaking changes в этих типах должны выходить отдельной версией пакета.

## Агрегаты и invariants

### Organization

- имя обязательно;
- slug нормализуется детерминированно;
- creator атомарно становится `owner/ACTIVE`;
- сохраняется минимум один активный owner;
- membership mutations увеличивают aggregate version;
- агрегат выдаётся из repository только detached copy.

### Membership

```text
INVITED → ACTIVE → SUSPENDED → REMOVED
    └──────────────────────────→ REMOVED
```

Invitation не даёт доступа. Доступ появляется только после acceptance. Повторный invite разрешён лишь после `REMOVED`.

### Operation

```text
PENDING → RUNNING → WAITING_EXTERNAL → RUNNING → SUCCEEDED
   │          │             │
   ├──────────┼─────────────┼────────→ FAILED
   └──────────┴─────────────┴────────→ CANCELED
```

- terminal state immutable;
- cancel идемпотентен;
- failed result обязан иметь стабильный error code;
- optimistic version предотвращает lost update.

## Authorization

Проверка является пересечением двух политик:

1. caller scope разрешает action;
2. активная membership role разрешает action.

Дополнительно:

- tenant context выводится из target resource, а не из request/OIDC tenant claim;
- cross-tenant context всегда отклоняется;
- invitation и suspended membership доступа не дают;
- admin не может назначать, менять или удалять owner membership;
- developer не может управлять memberships.

## Idempotent command transaction

Каждая mutation выполняется в одной transaction:

```text
claim (scope, idempotency_key, fingerprint)
    ↓
load/check aggregate + authorization
    ↓
domain mutation
    ↓
operation state records
    ↓
outbox events
    ↓
audit record + audit event
    ↓
complete idempotency record with original response
    ↓
COMMIT
```

Семантика:

- same key + same canonical payload → original response;
- same key + different payload → `IDEMPOTENCY_CONFLICT`;
- transaction rollback удаляет и claim, и side effects;
- concurrent same-key commands получают одного winner;
- lost HTTP response восстанавливается replay того же результата.

## Outbox и inbox

Outbox использует lease-based claim:

```text
PENDING → DISPATCHING → PUBLISHED
                 └────→ PENDING on publish failure
```

Если publish успешен, но marking в БД не произошёл, event будет опубликован повторно после lease expiry. Поэтому delivery — at-least-once, а consumer обязан применять inbox deduplication.

Inbox хранит `(event_id, canonical fingerprint)`. Повтор того же event игнорируется; тот же `event_id` с другим payload отклоняется.

## Audit

Audit содержит:

- actor;
- tenant;
- action;
- resource;
- correlation ID;
- outcome;
- stable error code;
- redacted metadata;
- timestamp.

На repository boundary доступны только append/list. В PostgreSQL UPDATE и DELETE блокируются triggers. Sensitive keys рекурсивно заменяются на `[REDACTED]`; internal causes не сериализуются в public errors.

## HTTP API

Реализованы:

```text
POST   /v1/organizations
GET    /v1/organizations/{organizationID}
POST   /v1/organizations/{organizationID}/invitations
POST   /v1/organizations/{organizationID}/invitations/accept
PATCH  /v1/organizations/{organizationID}/members/{principalID}/role
POST   /v1/organizations/{organizationID}/members/{principalID}/suspend
DELETE /v1/organizations/{organizationID}/members/{principalID}
GET    /v1/organizations/{organizationID}/audit
GET    /v1/operations/{operationID}
POST   /v1/operations/{operationID}/cancel
GET    /healthz
```

Mutation endpoints требуют `Idempotency-Key`. JSON decoder:

- ограничивает body до 1 MiB;
- отклоняет unknown fields;
- отклоняет trailing JSON objects;
- не возвращает stack traces, SQL errors и provider credentials.

Header authentication в `cmd/kernel-api` предназначена только для local development. Production OIDC middleware остаётся отдельной задачей.

## PostgreSQL integration suite

Под build tag `postgres_integration` подготовлены реальные тесты:

```text
TestPostgres_Migrations_CleanInstallAndUpgrade
TestPostgres_CreateOrganizationAndOutboxAreAtomic
TestPostgres_ConcurrentIdempotencyUsesSingleWinner
TestPostgres_OperationOptimisticLockPreventsLostUpdate
TestPostgres_AuditTableRejectsUpdateAndDelete
```

Suite использует локальный CGO/libpq test driver без внешней Go-зависимости.

Запуск:

```bash
export TEST_POSTGRES_DSN='host=127.0.0.1 port=5432 user=postgres password=postgres dbname=kernel_test sslmode=disable'
make test-postgres
```

Тестовый пользователь должен иметь право создавать и удалять schema `kernel`. Suite разрушительно сбрасывает только эту schema в тестовой database.

## Быстрый запуск

```bash
make verify
make fuzz
make coverage-html
make build

KERNEL_HTTP_ADDR=127.0.0.1:8080 ./bin/kernel-api
```

Полная локальная проверка:

```bash
./scripts/verify-iteration-1.sh
```
