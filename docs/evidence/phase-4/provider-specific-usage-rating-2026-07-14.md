# Provider-specific usage rating evidence — 2026-07-14

Evidence class: `LOCAL_GREEN`

Requirement: `C11` — `TestUsage_ApifyAndBrightDataMetersRemainProviderSpecificButInvoiceStable`.

The additive Commerce v1 meter catalog now includes separate OpenRouter input
and output tokens, Apify actor runs and storage bytes, and Bright Data requests
and transfer bytes. Existing meters and payloads remain unchanged.

The executable rating test ingests Apify and Bright Data facts in both orders
and proves byte-identical invoice previews. It also proves the facts remain two
provider-specific lines and rate to a deterministic 80 minor-unit total using
integer arithmetic (75 Apify + 5 Bright Data).

Verification commands:

```text
go test -race ./internal/commerce/application \
  -run '^TestUsage_ApifyAndBrightDataMetersRemainProviderSpecificButInvoiceStable$' -count=1
go test ./test/contract ./internal/commerce/application
go test ./...
go vet ./...
```

This is local ledger/rating evidence. Live provider attribution and usage
deduplication remain capability-provider gates.
