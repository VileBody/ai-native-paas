# 00. Общие контракты и тестовый harness

## Назначение

Этот документ не является отдельной продуктовой итерацией. Он задаёт одинаковые правила для всех семи bounded contexts, чтобы тесты не зависели от случайной реализации.

## Базовые value objects

```go
type TenantID string
type PrincipalID string
type OperationID string
type CorrelationID string
type IdempotencyKey string

type PrincipalContext struct {
    PrincipalID PrincipalID
    TenantID    TenantID
    Kind        string // user | agent | service
    Scopes      []string
}
```

Value objects обязаны:

- валидировать формат на границе;
- не принимать empty value;
- не содержать database logic;
- иметь стабильную JSON-форму;
- не раскрывать provider-specific identifiers без необходимости.

## Event envelope

```go
type EventEnvelope[T any] struct {
    EventID      string        `json:"event_id"`
    Type         string        `json:"type"`
    Version      int           `json:"version"`
    TenantID     string        `json:"tenant_id"`
    AggregateID  string        `json:"aggregate_id"`
    CorrelationID string       `json:"correlation_id"`
    CausationID  string        `json:"causation_id"`
    OccurredAt   time.Time     `json:"occurred_at"`
    Payload      T             `json:"payload"`
}
```

Обязательные contract tests:

```text
TestEventEnvelope_RequiredFieldsAreSerialized
TestEventEnvelope_UnknownFieldsAreIgnoredByV1Consumer
TestEventEnvelope_V1SchemaIsBackwardCompatible
TestEventEnvelope_DoesNotSerializeSecrets
```

## Error contract

Публичные ошибки имеют стабильный код:

```json
{
  "code": "SOURCE_REPOSITORY_NOT_READY",
  "message": "Repository is not ready",
  "operation_id": "op_...",
  "retryable": true,
  "details": {}
}
```

Нельзя отдавать пользователю:

- SQL error;
- stack trace;
- Kubernetes object dump;
- provider token;
- internal hostname;
- secret value.

Обязательные тесты:

```text
TestPublicError_MapsDomainErrorToStableCode
TestPublicError_RedactsInternalCause
TestPublicError_RetryableFlagMatchesPolicy
```

## Idempotency contract

Для mutation command вычисляется fingerprint канонизированного payload.

Поведение:

- тот же key + тот же fingerprint → исходный результат;
- тот же key + другой fingerprint → `IDEMPOTENCY_CONFLICT`;
- concurrent same key → ровно одна side effect;
- provider retry использует тот же provider idempotency key.

Обязательные тесты:

```text
TestIdempotency_ReplayReturnsOriginalResponse
TestIdempotency_PayloadMismatchConflicts
TestIdempotency_ConcurrentRequestsCreateOneEffect
TestIdempotency_FailedRetryableOperationCanResume
```

## Clock и deterministic tests

Все домены получают:

```go
type Clock interface {
    Now() time.Time
}

type IDGenerator interface {
    New(prefix string) string
}
```

Тесты не вызывают `time.Now()` и не используют реальные ожидания.

## Database boundaries

Каждый домен владеет своей PostgreSQL schema.

Запрещено:

- cross-schema join из production code;
- cross-schema foreign key;
- общий repository object на несколько доменов;
- обновление чужой таблицы;
- чтение чужого event outbox напрямую.

Разрешено:

- подписка на versioned event;
- вызов versioned application port;
- собственная denormalized projection.

Обязательные architecture tests:

```text
TestArchitecture_NoCrossDomainInternalImports
TestArchitecture_NoCrossSchemaSQLReferences
TestArchitecture_OnlyContractsPackageIsShared
```

## Test doubles

Используются три разных класса:

- `Fake` — рабочая in-memory реализация порта;
- `Stub` — заранее заданный ответ;
- `Spy` — фиксирует вызовы для orchestration test.

Не использовать один огромный mock object для всех интеграций.

## Fixture catalog

Создать стабильные fixtures:

```text
/test/fixtures/source/hello-go
/test/fixtures/source/hello-node
/test/fixtures/source/hello-python
/test/fixtures/source/invalid-node
/test/fixtures/source/malicious-symlink
/test/fixtures/source/dockerfile-safe
/test/fixtures/source/dockerfile-network-attempt
/test/fixtures/events
/test/fixtures/gitops
/test/fixtures/crd
```

Fixtures versioned и не должны динамически скачивать зависимости во время unit tests.

## CI suites

### Fast suite

- domain;
- application;
- architecture;
- serialization contracts.

### Integration suite

- PostgreSQL;
- HTTP provider stubs;
- local OCI registry;
- Kubernetes `envtest`.

### System suite

- ephemeral `kind` cluster;
- Argo CD;
- PaaS operator;
- sample app rollout.

### Nightly provider suite

- real disposable GitLab project;
- real kpack build;
- real Harbor project;
- real Cozystack sandbox tenant, если доступен.

## Golden files

Golden files допустимы только для:

- rendered GitOps YAML;
- CRD schema;
- public JSON contracts;
- invoice/rating output.

Golden update требует явного флага и review diff. Автоматическое обновление в обычном тестовом запуске запрещено.

## Redaction assertions

Каждый integration/acceptance test, который использует secret fixture, после выполнения проверяет:

```text
secret value not in logs
secret value not in events
secret value not in GitOps repository
secret value not in build metadata
secret value not in public error
```
