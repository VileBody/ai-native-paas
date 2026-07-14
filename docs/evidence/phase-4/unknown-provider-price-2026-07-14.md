# Unknown provider price range and approval — 2026-07-14

Evidence class: `LOCAL_GREEN`.

Requirement: `C2` —
`TestCost_UnknownProviderPriceProducesRangeAndApprovalRequirement`.

An OpenTofu change whose provider/resource type has no reliable exact price is
never represented as a zero-cost exact estimate. The canonical test proves an
unknown resource produces:

- an estimate line with `price_known=false`;
- a conservative customer range of RUB 0 through RUB 12,500.00 under the beta
  fallback ceiling and 25% markup;
- `approval_required=true` on both estimate and plan summary;
- an apply rejection with `ErrApprovalRequired` when no exact-plan grant is
  supplied, even for staging.

The additive commerce v2 estimate now exposes the immutable pricing inputs
needed for audit and UI: rate-card version, provider price-snapshot ID and
markup basis points. Estimate-version and plan-idempotency fingerprints bind
rate-card ID/version, snapshot ID, markup, currency and canonical plan hash.

Verification:

```text
go test -count=1 ./test/pivot \
  -run '^TestCost_UnknownProviderPriceProducesRangeAndApprovalRequirement$' -v
go test -count=1 ./...
go test -race -count=1 ./...
go vet ./...
make fmt-check generate-check
```

Matrix result:

- 157 requirements;
- 990 discovered Go test/fuzz targets;
- `REUSED 102`, `NEW 0`, `LIVE_ONLY 55`;
- zero unmapped requirements;
- C2 now maps to executable unknown-price and approval evidence.

The beta fallback ceiling is conservative policy, not a provider quote. Live
Timeweb price ingestion/versioning remains a provider-data gate.
