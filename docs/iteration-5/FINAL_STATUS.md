# Iteration 5 — Final Status

Generated: `2026-07-12T23:43:52Z`

## Verdict

```text
Iteration:                  5 — Application Attachments
Overall status:             RED
Mandatory TDD parity:       0/0
Missing mandatory tests:    0
Attachment top-level tests: 0
Targeted statement coverage:unknown
Live PostgreSQL:            NOT RUN
Live Kubernetes:            NOT RUN
Live external providers:    NOT RUN
```

`GREEN` means every locally enforceable gate and live PostgreSQL gate passed. It does **not** claim verification against a real Kubernetes API server, OpenBao, Cozystack, authoritative DNS provider, ACME issuer or cert-manager.

## Frozen output contract

The bounded context exports an immutable attachment snapshot containing references only:

```go
type AttachmentSnapshot struct {
    SnapshotID      string
    TenantID        string
    ApplicationID   string
    EnvironmentID   string
    Version         int64
    SecretSetRef    string
    ServiceBindings []string
    ActiveDomains   []string
    CreatedAt       time.Time
}
```

No plaintext secret, database password, private key or provider credential may cross this contract.

## Gate results

| Gate | Result |
|---|---|
| `gofmt` | MISSING |
| `vet` | MISSING |
| `default_suite` | MISSING |
| `attachments_suite` | MISSING |
| `attachments_race` | MISSING |
| `attachments_shuffle` | MISSING |
| `attachments_coverage` | MISSING |
| `attachments_no_skips` | MISSING |
| `tdd_parity` | MISSING |
| `postgres_tag_compile` | MISSING |
| `no_cross_schema_sql` | MISSING |
| `no_secret_values_in_contract` | MISSING |
| `immutable_runtime_reference_scan` | MISSING |
| `postgres_live` | NOT RUN |

## Failed or missing gates

```text
Missing: gofmt, vet, default_suite, attachments_suite, attachments_race, attachments_shuffle, attachments_coverage, attachments_no_skips, tdd_parity, postgres_tag_compile, no_cross_schema_sql, no_secret_values_in_contract, immutable_runtime_reference_scan
Failed:  none
```

Full raw logs are in `.verification/final/`.
