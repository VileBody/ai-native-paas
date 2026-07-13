# Iteration 6 — восстановление baseline

## Что было доступно

После сброса инструментального sandbox полный исходный workspace Iteration 6 был утрачен. В активном окружении сохранились:

- полный cumulative source package Iterations 1–4;
- отдельный частично восстановленный `commerce` bounded context;
- архив Iteration 5, содержащий документацию и frozen handoff, но **не исходный код Attachments**.

Поэтому Iteration 6 восстановлена как:

```text
verified cumulative repository Iterations 1–4
        +
Commercial Governance implementation
        +
Iteration 5 frozen handoff/contract assumptions
```

## Что это означает

Commercial Governance не импортирует `internal/attachments`, не читает schema `attachments` и не требует её persistence model. Связь с Attachments определена исключительно через внешние identifiers, entitlement decisions и usage events. Поэтому собственные domain, application, PostgreSQL, HTTP и acceptance gates Iteration 6 проверяются полноценно.

Однако этот deliverable **не доказывает compile/integration совместимость с фактической реализацией Iteration 5**, поскольку её source tree отсутствовал в доступном артефакте. Такой gate нужно выполнить после получения полного исходного пакета Iteration 5:

```text
merge full Iteration 5 source
        ↓
go test ./...
        ↓
go test -race ./...
        ↓
attachments → commerce contract acceptance
```

## Запрещённый обход

В Iteration 6 не создавались фиктивные таблицы, adapters или внутренние packages Attachments для имитации отсутствующего source tree. Это сохранило честную bounded-context boundary и не спрятало проблему baseline за stub-реализацией.
