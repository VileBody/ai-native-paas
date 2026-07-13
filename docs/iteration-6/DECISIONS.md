# Iteration 6 — архитектурные решения

## ADR-6.1: внутренний ledger является источником истины

Платёжный provider не определяет quota, entitlement или usage semantics. Он позже получает уже рассчитанные invoice lines. Это позволяет менять PSP без изменения runtime/build domains.

## ADR-6.2: price и money — только integer/rational

Floating-point запрещён в public contract, domain и persistence. Цена хранится как `minor_units / per_quantity`; промежуточные операции используют overflow-safe integer arithmetic.

## ADR-6.3: reservation precedes side effect

Проверки вида «сначала создать pod, затем проверить quota» запрещены. Consumer сначала получает reservation, после внешнего результата выполняет commit/release.

## ADR-6.4: usage — append-only fact

Ошибки не исправляют исходную запись. Корректировка создаётся отдельным `CORRECTION`/`CREDIT` event. Это сохраняет воспроизводимость invoice и audit.

## ADR-6.5: billing period pin-ит plan version

Новый тариф не меняет стоимость уже принятого usage. Period хранит exact version, а active plan immutable.

## ADR-6.6: ownership — внешний fail-closed port

Commerce не читает tables Runtime/Build/Attachments. Producer identity проверяется через `ResourceOwnership`. Отсутствие resolver означает deny, а не allow.

## ADR-6.7: suspension не является deletion

Commercial state выпускает intent, но не владеет customer data. Runtime останавливает compute, Attachments сохраняет managed services, а purge остаётся отдельной destructive operation.

## ADR-6.8: invoice preview не является mutable invoice

В Iteration 6 строится deterministic preview из ledger. Нумерация invoices, налоги, PSP charge и accounting export относятся к отдельному последующему контуру.

## ADR-6.9: отдельная schema без cross-domain FK

`commerce` хранит внешние IDs как values. Это предотвращает каскадные migrations и сохраняет независимость bounded contexts.

## ADR-6.10: отсутствие исходников Iteration 5 не маскируется stubs

Iteration 6 проверяется против frozen public boundary. Полная cumulative интеграция с Attachments остаётся отдельным обязательным gate после восстановления фактического source tree.
