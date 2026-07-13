# Итерация 1 — отдельный TODO

Здесь перечислено только то, что не удалось полноценно проверить или сознательно не вошло в текущую реализацию.

## P0 — закрыть exit gate итерации

### 1. Выполнить live PostgreSQL integration suite

**Статус:** suite написан и компилируется, но в текущем окружении отсутствуют PostgreSQL server, container runtime и `TEST_POSTGRES_DSN`. Поэтому пять тестов запускаются как `SKIP`.

Нужно выполнить:

```bash
export TEST_POSTGRES_DSN='host=127.0.0.1 port=5432 user=postgres password=postgres dbname=kernel_test sslmode=disable'
make test-postgres
```

Обязательные green-тесты:

```text
TestPostgres_Migrations_CleanInstallAndUpgrade
TestPostgres_CreateOrganizationAndOutboxAreAtomic
TestPostgres_ConcurrentIdempotencyUsesSingleWinner
TestPostgres_OperationOptimisticLockPreventsLostUpdate
TestPostgres_AuditTableRejectsUpdateAndDelete
```

Пока они не green на реальном PostgreSQL, exit gate `outbox atomicity proven by real Postgres` формально не закрыт.

### 2. Зафиксировать migration rollback policy

Текущие migrations checksummed, transactional и forward-only. Автоматический down migration сознательно не добавлен, чтобы не поощрять destructive rollback.

Нужно выбрать и документировать один production policy:

- forward-fix only + database restore procedure;
- либо explicitly reviewed down migrations для reversible changes.

После решения добавить rehearsal test восстановления предыдущей версии schema/data.

## P1 — до production deployment

### 3. Подключить production PostgreSQL driver и storage wiring

`cmd/kernel-api` сейчас использует только in-memory adapter. PostgreSQL adapter driver-neutral и принимает `*sql.DB`, но executable ещё не открывает production connection, не запускает migrations и не настраивает pool/timeouts.

Нужно:

- выбрать driver;
- отдельные runtime/migration database roles;
- pool limits и connection lifetime;
- startup readiness;
- graceful drain;
- migration lock для нескольких replicas.

### 4. Подключить полноценный OIDC verifier

Текущий `adapters/oidc.Mapper` получает уже verified claims и проверяет issuer/subject/kind. Не проверено в текущем окружении:

- JWT signature;
- JWKS rotation/cache;
- `aud`/`azp`;
- `exp`/`nbf`/clock skew;
- nonce/state;
- token revocation/session logout;
- Keycloak integration.

До этого header-based development authentication нельзя выставлять наружу.

### 5. Реальный broker contract/integration test

Outbox semantics полностью проверены на fake publisher, включая publish-success/mark-failure retry. Не проверено:

- конкретный NATS/Kafka transport;
- connection loss/ack timeout;
- event size limits;
- broker ACL;
- consumer restart against real broker.

### 6. Metrics и operational alerts

Нужно добавить и проверить:

- command latency/error counters;
- idempotency conflict/in-progress counters;
- outbox pending age, retries и poison events;
- audit append failures;
- PostgreSQL pool saturation;
- operation state duration;
- alert thresholds.

### 7. Edge protection

Не реализованы:

- per-principal/tenant rate limits;
- global load shedding;
- request deadline policy;
- IP/device abuse controls;
- maximum concurrent in-flight mutations.

## P2 — hardening

### 8. PostgreSQL defense in depth

Рассмотреть:

- RLS по tenant для read paths;
- отдельный append-only audit role;
- external/WORM audit export;
- table partitioning и retention;
- encrypted backups и PITR restore drills.

### 9. Extended chaos and soak

Текущий максимум: race detector, 64-worker idempotency/inbox concurrency и focused fuzzing.

Ещё не выполнены:

- process kill после database commit до HTTP response;
- process kill после broker publish до outbox mark;
- network partition PostgreSQL/broker;
- sustained 1k+ concurrent command soak;
- multi-hour fuzz campaign;
- transaction deadlock/serialization fault injection на real PostgreSQL.

### 10. Public contract artifacts

Go v1 contracts и serialization tests готовы. При публикации внешнего API дополнительно нужны:

- versioned OpenAPI document;
- standalone JSON Schemas для events;
- compatibility checker in CI;
- deprecation policy.
