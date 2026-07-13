# Итерация 1 — отчёт о тестировании

## Итог

**Статус функционального контура:** green.

**Статус формального exit gate:** conditional green — все проверки, доступные в текущем окружении, пройдены; пять подготовленных тестов требуют живого PostgreSQL и в этом контейнере были корректно пропущены из-за отсутствия PostgreSQL server и `TEST_POSTGRES_DSN`.

Проверка выполнена 12 июля 2026 года на:

```text
Go:       go1.23.2 linux/amd64
OS:       Linux x86_64
Compiler: GCC 14.2.0
libpq:    17.9
```

## Покрытие исходного TDD-плана

В `docs/tdd/01-platform-kernel.md` перечислено 40 обязательных тестов. Все 40 реализованы с теми же именами.

Всего в репозитории:

```text
95  именованных Test... функций
 5  из них — live PostgreSQL tests под build tag
90  обычных Test... функций в default suite
 1  fuzz target
```

Обычный запуск `go test -json -count=1 ./...` дал:

```text
91  прошедшая top-level проверка
109 прошедших test events с учётом subtests
 0  failures
 0  skips в default suite
```

## Единый verification gate

Команда:

```bash
FUZZTIME=5s ./scripts/verify-iteration-1.sh
```

Результат:

```text
fmt-check                    PASS
go vet ./...                 PASS
go vet с postgres tag        PASS
go test ./...                PASS
go test -race ./...          PASS
PostgreSQL suite compilation PASS
focused fuzz, 5 seconds      PASS, 230757 executions
all-package coverage         PASS, 60.1% statements
binary build                 PASS
```

Скрипт завершился сообщением:

```text
Iteration 1 verification completed successfully.
```

## Дополнительные стабильностные прогоны

### Shuffled repeat

Проверены kernel, memory, HTTP, OIDC, contracts и acceptance:

```bash
go test -shuffle=on -count=50 \
  ./internal/kernel/... \
  ./adapters/oidc \
  ./test/contract \
  ./test/acceptance
```

Результат: **PASS**. Зависимости от порядка тестов и flaky failures не обнаружены.

### Race + shuffled repeat

```bash
go test -race -shuffle=on -count=10 \
  ./internal/kernel/... \
  ./test/acceptance
```

Результат: **PASS**. Data races не обнаружены.

### Расширенный fuzz-run

```bash
go test ./internal/kernel \
  -run='^$' \
  -fuzz=FuzzOperationTransitionNeverMutatesOnRejectedTransition \
  -fuzztime=20s
```

Результат:

```text
1062111 executions
0 new failures
PASS
```

Fuzz target проверяет, что запрещённый transition операции не изменяет состояние и version агрегата.

## Coverage

Команда:

```bash
go test -count=1 \
  -covermode=atomic \
  -coverpkg=./... \
  -coverprofile=coverage.out \
  ./...
```

Итог:

```text
all packages:                         60.1% statements
internal/kernel:                      67.7%
adapters/oidc:                        92.6%
internal/kernel/memory:               55.1%
internal/kernel/httpapi package-only: 22.9%
adapters/postgres/kernel:              8.5%
```

Низкое package-only покрытие HTTP и PostgreSQL не означает отсутствие внешних сценариев:

- HTTP дополнительно исполняется из отдельного acceptance package и входит в all-package profile;
- основные PostgreSQL ветви требуют живого сервера и вынесены в tagged integration suite.

HTML-отчёт: `coverage.html`.

## HTTP process smoke test

Собран и запущен реальный процесс:

```bash
KERNEL_HTTP_ADDR=127.0.0.1:18080 ./bin/kernel-api
```

Проверено:

```text
GET /healthz
→ 200 {"status":"ok"}

POST /v1/organizations
→ 202
→ operation state SUCCEEDED
→ normalized slug smoke-acme
→ creator membership owner/ACTIVE
```

Процесс корректно принял SIGTERM и завершился через graceful shutdown path.

## PostgreSQL integration suite

Под build tag `postgres_integration` подготовлены и успешно компилируются:

```text
TestPostgres_Migrations_CleanInstallAndUpgrade
TestPostgres_CreateOrganizationAndOutboxAreAtomic
TestPostgres_ConcurrentIdempotencyUsesSingleWinner
TestPostgres_OperationOptimisticLockPreventsLostUpdate
TestPostgres_AuditTableRejectsUpdateAndDelete
```

В текущем окружении команда:

```bash
CGO_ENABLED=1 go test \
  -count=1 \
  -tags=postgres_integration \
  ./test/integration -v
```

дала:

```text
5 SKIP: TEST_POSTGRES_DSN is not set
package PASS
```

Это единственный незакрытый обязательный инфраструктурный тестовый слой. Для реального запуска:

```bash
export TEST_POSTGRES_DSN='host=127.0.0.1 port=5432 user=postgres password=postgres dbname=kernel_test sslmode=disable'
make test-postgres
```

CI workflow содержит отдельный job с PostgreSQL 17 service container.

## Exit-gate matrix

| Gate | Статус | Доказательство |
|---|---:|---|
| Все обязательные TDD test names реализованы | PASS | 40/40 exact-name match |
| Domain/application suite | PASS | default suite |
| Cross-tenant negative suite | PASS | domain, service, HTTP acceptance |
| Concurrent idempotency | PASS | in-memory 64-worker tests + race |
| Operation transition matrix | PASS | table/property/fuzz tests |
| Outbox/inbox semantics | PASS | atomic fake-store tests, retries, deduplication |
| Audit redaction | PASS | recursive redaction + public-error tests |
| Append-only audit на repository boundary | PASS | API shape/reflection tests |
| API acceptance scenario | PASS | owner → invite → accept → developer access → audit |
| Build/vet/race | PASS | unified verification script |
| Real PostgreSQL atomicity | **NOT RUN** | suite ready; no server/DSN in current environment |
| PostgreSQL immutable audit triggers | **NOT RUN** | suite ready; no server/DSN in current environment |

## Артефакты

```text
coverage.out
coverage.html
bin/kernel-api
docs/iteration-1/IMPLEMENTATION.md
docs/iteration-1/THREAT_MODEL.md
docs/iteration-1/TODO.md
```

SHA-256 на момент отчёта:

```text
35af6c8796c2b140c6942738f34acb2f1f4da376901835e44b088858019f67ba  bin/kernel-api
a1995371404a029c5a742a21e2981c2ab2fea5cf72ebfbeada98c014776c1f51  coverage.out
5cfb6cf3723dbf80dc4d833a0f48295913f711a9f464929a34beb3b2c84aa193  coverage.html
```

## Решение о готовности

Первую итерацию можно использовать как основу для разработки второй итерации и для локального/CI TDD. Для production deployment и формального полного закрытия Iteration 1 необходимо выполнить P0 из `TODO.md`, прежде всего прогнать пять live PostgreSQL tests на реальном PostgreSQL.
