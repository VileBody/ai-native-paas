# Iteration 6 — Test report

## Summary

```text
Mandatory TDD tests:                 52 / 52 PASS
Commerce-related top-level tests:    88
Repository top-level Test functions: 508
Commerce fuzz targets:                2
Confirmed fuzz executions:      116,473
Commerce PostgreSQL tests:       10 / 10 PASS
Cumulative PostgreSQL tests:          32 PASS
Untagged Commerce coverage:         59.4%
PostgreSQL-tagged coverage:         46.8%
Skips in final verification:            0
Failures in final verification:         0
```

## Final verification command

```bash
PAAS_TEST_POSTGRES_BINDIR=/path/to/postgresql/18/bin \
PAAS_TEST_POSTGRES_SHAREDIR=/path/to/postgresql/18/share \
PAAS_TEST_POSTGRES_LD_LIBRARY_PATH=/path/to/required/compat-libs \
REQUIRE_POSTGRES=1 \
RUN_CUMULATIVE_RACE=1 \
RUN_CUMULATIVE_POSTGRES=1 \
FUZZTIME=5s \
./scripts/verify-iteration-6.sh
```

При системном PostgreSQL достаточно `TEST_POSTGRES_DSN` или обнаруживаемых `pg_config/initdb`.

## Gate matrix

| Gate | Result |
|---|---|
| `gofmt` | PASS |
| `go vet ./...` | PASS |
| tagged PostgreSQL vet | PASS |
| default cumulative suite | PASS |
| targeted Commerce race | PASS |
| cumulative race | PASS |
| shuffle ×20 / acceptance ×10 | PASS |
| mandatory TDD parity | 52/52 PASS |
| no floating-point money | PASS |
| no cross-domain SQL | PASS |
| no cross-domain internal import | PASS |
| fuzz runtime usage | 54,971 executions, PASS |
| fuzz rating arithmetic | 61,502 executions, PASS |
| Commerce coverage | 59.4% |
| binaries build | PASS |
| `commerce-api` process smoke | PASS |
| live PostgreSQL Commerce | 10/10 PASS under race |
| live PostgreSQL cumulative | 32 PASS under race |

## Process smoke

Реально запущенный `commerce-api` прошёл следующий workflow:

```text
GET /healthz
create plan definition
create + activate plan version
start subscription and billing period
check entitlement
reserve + commit quota
append one usage event twice
verify single invoice line and exact amount
suspend account while retaining managed services
reject cross-tenant request with 403
verify Cache-Control: no-store
```

## Live PostgreSQL coverage

Проверены:

```text
TestPostgres_CommerceMigrationsCleanInstallAndUpgrade
TestPostgres_CommerceSubscriptionPeriodAccountAndOutboxAreAtomic
TestPostgres_CommerceConcurrentQuotaReservationsCannotOversubscribe
TestPostgres_CommerceConcurrentUsageIdempotencyUsesSingleWinner
TestPostgres_CommerceOptimisticLockPreventsLostAccountUpdate
TestPostgres_CommerceActivePlanAndPeriodPriceVersionAreImmutable
TestPostgres_CommerceUsageAuditAndIdempotencyAreAppendOnly
TestPostgres_CommercePayloadConsistencyRejectsColumnDrift
TestPostgres_CommerceSchemaUsesNoFloatingMoneyColumns
TestPostgres_CommerceInvoicePreviewIsDeterministic
```

Database tests выполнялись на PostgreSQL 18.4 с Go race detector. Cumulative run также повторно проверил persistence gates Kernel, Build и Runtime.

## Concurrency evidence

- memory test запускает конкурентные quota reservations и не допускает oversubscription;
- PostgreSQL test воспроизводит тот же сценарий при Serializable isolation;
- store выполняет bounded retries на serialization/deadlock errors;
- idempotent usage под concurrency создаёт единственного winner;
- account updates защищены optimistic version.

## Coverage qualification

Обычный profile намеренно показывает PostgreSQL adapter как 0%, поскольку tagged integration tests не входят в untagged command. Отдельный live PostgreSQL profile покрывает adapter и даёт **46.8%** по `internal/commerce/...`. Оба HTML-отчёта включены в пакет.

## Evidence

- `docs/iteration-6/VERIFICATION.log`;
- `docs/iteration-6/evidence/tdd-parity.json`;
- `docs/iteration-6/evidence/coverage-commerce.txt`;
- `docs/iteration-6/evidence/coverage-commerce-postgres.txt`;
- `docs/iteration-6/evidence/commerce-api-process.log`;
- `coverage-commerce.html`;
- `coverage-commerce-postgres.html`;
- `ITERATION_6_RESULT.json`;
- `ITERATION_6_STATUS.txt`.
