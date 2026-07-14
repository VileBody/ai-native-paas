# GitLab rate-limit recovery — 2026-07-14

Status: `LOCAL_GREEN`; GitLab adapter/provider contract is green, while the
live GitLab.com tenant-isolation gates remain pending.

Implemented:

- GitLab `GET` and `HEAD` discovery calls retry `429 Too Many Requests` with a
  fixed attempt budget and context cancellation.
- Retry timing follows GitLab's documented headers: `RateLimit-ResetTime`
  first, then `Retry-After`; missing or invalid headers use bounded exponential
  fallback.
- Defaults are three retries and a 30-second per-delay cap. Both values and the
  sleeper are injectable for deterministic tests.
- Non-idempotent `POST` requests are never retried by the HTTP adapter.
- Repository provisioning handles a `429` returned after project creation by
  discovering the project through its exact correlation marker. If discovery
  is itself rate-limited, only that safe `GET` is retried.
- Resuming the repository operation after recovery returns the same numeric
  provider identity and does not issue a second create request.

The header semantics match the official
[GitLab rate-limit documentation](https://docs.gitlab.com/administration/settings/user_and_ip_rate_limits/).

Executable evidence:

- `TestSource_GitLab429UsesBoundedRetryAndPreservesIdempotency`
- `TestGitLab_RateLimitRetryIsBoundedAndHonorsRetryAfter`
- `TestGitLab_NonIdempotentPostIsNotRetriedAfter429`
- `go test ./...`
- `go vet ./...`
- pivot matrix regeneration and `--check`

Matrix effect:

- S15 is now `REUSED` executable provider-contract evidence.
- The complete matrix remains mapped at 157 requirements.
- Discovered Go test/fuzz targets: 896.
- Statuses: `REUSED 43`, `NEW 44`, `LIVE_ONLY 70`.

Not claimed:

- No deliberate rate-limit event was generated against GitLab.com.
- GitLab.com token isolation, rename/transfer and archive gates remain pending.
