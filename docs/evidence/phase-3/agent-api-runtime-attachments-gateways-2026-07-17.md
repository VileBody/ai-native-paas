# Agent API runtime and attachments gateways — 2026-07-17

Production `agent-api` now uses tenant-scoped HTTP bridges when
`RUNTIME_API_URL` and `ATTACHMENTS_API_URL` are configured. Empty URLs preserve
the existing dependency-wait behavior.

The runtime bridge covers deploy, status, deployment-scoped rollback and
lost-response reconciliation. It carries idempotency, correlation and expected
environment revision, validates returned deployment identity/state, maps
client errors to non-retryable Agent domain errors and maps rate-limit/server
failures to retryable provider failures.

The attachments bridge covers write-only secret set/list-metadata, managed
service provision, bind and domain claims. It validates tenant/application/
environment scope on returned resources, never reflects an upstream secret
body or error, and zeroes its temporary serialized request bytes after use.

Both bridges use the shared internal mTLS client in the production composition
root. Unit tests also prove they remain fail-closed when their URL is absent.
