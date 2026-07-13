# Iteration 5 — TODO

## Environment-independent defects discovered by verification

## Gate `gofmt`

```text
No log available
```

## Gate `vet`

```text
No log available
```

## Gate `default_suite`

```text
No log available
```

## Gate `attachments_suite`

```text
No log available
```

## Gate `attachments_race`

```text
No log available
```

## Gate `attachments_shuffle`

```text
No log available
```

## Gate `attachments_coverage`

```text
No log available
```

## Gate `attachments_no_skips`

```text
No log available
```

## Gate `tdd_parity`

```text
No log available
```

## Gate `postgres_tag_compile`

```text
No log available
```

## Gate `no_cross_schema_sql`

```text
No log available
```

## Gate `no_secret_values_in_contract`

```text
No log available
```

## Gate `immutable_runtime_reference_scan`

```text
No log available
```

## Gate `postgres_live`

Exit: `None`

```text
No log available
```


## Real-provider acceptance

- OpenBao: auth method, namespace/policy isolation, lease renewal, revoke and HA failover.
- Cozystack: PostgreSQL/Redis/S3 create-discover-update-delete semantics and ambiguous timeout recovery.
- DNS provider: authoritative lookup, propagation behavior, DNSSEC/CNAME chains and rate limits.
- ACME/cert-manager: issuance, renewal, failed challenge, revocation and issuer rate limits.
- External Secrets Operator: refresh, deletion policy, rollout behavior and provider outage.

## Kubernetes-dependent acceptance

See `KUBERNETES_TODO.md`.

## Deferred cloud-lab backlog

The local/DB recovery intentionally defers these live infrastructure gates to
the shared cloud-lab backlog:

- real OpenBao authentication, tenant policies, lease renewal and HA failover;
- real Cozystack PostgreSQL/Redis/S3 lifecycle and ambiguous-timeout recovery;
- authoritative DNS propagation/rebinding tests and ACME staging issuance;
- External Secrets materialization and rollout behavior;
- provider callback authentication, outage chaos and regional failure tests;
- Kubernetes-backed Runtime/Commerce/Agent end-to-end acceptance.

Local fakes and provider contract adapters remain mandatory; only acceptance
against live provider installations is deferred.

## Production resilience and operations

- provider webhook/callback authentication against chosen products;
- retry budgets, circuit breakers and per-provider concurrency limits;
- backup/restore of attachment metadata and reconciliation after restore;
- provider outage and partial-region failure chaos tests;
- high-cardinality metrics and alert thresholds;
- secret-rotation soak tests;
- domain verification abuse/rate limiting;
- certificate-expiry and renewal alerts;
- retention/legal-hold policy for managed services;
- operator runbooks for stuck provisioning, binding and TLS states.
