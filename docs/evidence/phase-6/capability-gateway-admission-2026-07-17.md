# Capability gateway admission core — 2026-07-17

Evidence class: `PROVIDER_GREEN` input, not a live provider gate.

This change adds the provider-neutral capability gateway core used by the
OpenRouter-compatible, Apify and Bright Data adapters:

- gateway admission is bound to tenant, project, binding ID and capability
  kind;
- project callers present only the project-scoped token reference stored on the
  binding;
- provider master credentials are acquired through a short-lived broker lease
  and remain only in private upstream headers;
- missing gateway configuration, unavailable binding storage or unavailable
  credential leases fail closed as dependency waits;
- capability usage facts are translated into stable Commerce meters with
  provider-event-scoped idempotency keys.

The executable tests prove:

```text
go test ./internal/capability/gateway
```

- provider lease outage returns dependency code
  `capability_provider_credentials`;
- public binding output does not expose budget, rate-limit, token refs or
  provider master credentials;
- OpenRouter usage maps to input/output token meters while preserving provider
  request attribution and deduplication identity.

Remaining live gates:

- OpenBao/provider-gateway lease issuance against real OpenRouter, Apify and
  Bright Data credentials;
- real outbound provider calls through the admin/workspace governed egress
  path;
- provider usage replay against Commerce PostgreSQL;
- budget exhaustion and provider substitution under live provider failure.
