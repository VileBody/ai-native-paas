# Iteration 6 — TODO после локального exit gate

## Обязательный baseline gate

- восстановить полный source package Iteration 5;
- объединить его с этим repository;
- прогнать cumulative default/race/PostgreSQL suites;
- добавить contract acceptance `attachments allocation → entitlement → usage`;
- не исправлять Attachments semantics обходами внутри Commerce.

## Production identity/API

- заменить trusted headers на verified OIDC/service-to-service identity;
- добавить authorization policy для plan administration;
- добавить rate limits, request IDs и structured audit actor;
- versioned OpenAPI schema;
- pagination для usage/invoice data;
- production PostgreSQL wiring и pool configuration для `commerce-api`.

## Event transport

- durable usage ingestion через broker;
- producer signature/attestation;
- dead-letter and replay policy;
- outbox publisher, inbox consumer и lag alerts;
- deterministic backfill orchestration.

## Billing operations

- immutable issued Invoice aggregate;
- tax engine boundary;
- discounts только как explicit versioned rules/credits;
- PSP collection/refunds;
- dunning workflow;
- accounting export;
- currency exponent catalog и multi-currency policy.

## Metering integrations

- Runtime/Build/Attachments producers against frozen `UsageSink`;
- object storage and egress authoritative counters;
- log ingestion/retention meters;
- gap detection across cells/regions;
- late-event close/reopen policy.

## PostgreSQL operations

- HA/failover tests;
- connection pool sizing;
- migration lock for multiple replicas;
- PITR backup and restore drills;
- long-running billing-period performance;
- partitioning/retention design for usage ledger;
- isolation tests under production proxy/pooler.

## Resilience/security

- chaos during quota commit and usage append;
- soak with high-cardinality resource IDs;
- tenant-level abuse throttling;
- external tamper-evident audit sink;
- cost-anomaly alerts;
- reconciliation across regional clock skew.
