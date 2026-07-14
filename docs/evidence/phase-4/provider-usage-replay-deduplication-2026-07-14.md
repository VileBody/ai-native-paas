# Provider usage replay deduplication — 2026-07-14

Evidence class: `DB_GREEN`.

Requirement: `C9` —
`TestUsage_ProviderReportReplayDoesNotDoubleCharge`.

Commerce now exposes `IngestProviderUsage` for trusted provider observations.
Each report is bound to tenant, project, operation, provider, provider event ID,
resource identity, meter, quantity and observation window. The resource must pass
the project-aware ownership port before ingestion.

The immutable usage idempotency key is derived from `(provider,
provider_event_id)`. In one PostgreSQL transaction the service appends the usage
event, usage-idempotency record, outbox and audit records, then reads back the
persisted identity. An exact replay returns the same v2 `UsageFact`; the same
provider event ID with a changed quantity conflicts instead of silently
overwriting or adding a second charge.

The v2 fact carries `project_id`, `operation_id`, `provider` and
`provider_event_id`, preserving attribution and provider provenance without
making raw webhook payloads part of the billing contract.

The exact integration test ran against the isolated
`ai-native-paas-user-test/user-postgres` service and proved two identical reports
produce one row in `commerce.usage_events`. No admin database was used.

Verification:

```text
CGO_ENABLED=1 go test -tags=postgres_integration -count=1 ./test/integration \
  -run '^TestUsage_ProviderReportReplayDoesNotDoubleCharge$' -v
go test -count=1 ./...
go test -race -count=1 ./...
go vet ./...
make fmt-check generate-check
```

Matrix result:

- 157 requirements;
- 995 discovered Go test/fuzz targets;
- `REUSED 106`, `NEW 0`, `LIVE_ONLY 51`;
- zero unmapped requirements;
- C9 now maps to executable PostgreSQL replay evidence.

Provider webhook signature verification and transport retries remain provider
gateway/live gates; this slice implements the durable ingestion boundary they
feed.
