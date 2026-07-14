# Capability project rate-limit evidence — 2026-07-14

Evidence class: `LOCAL_GREEN`

Requirement: `A5.26` — `TestCapability_RateLimitIsPerProjectAndDoesNotLeakCrossTenantState`.

The capability gateway now has a concurrency-safe admission core keyed by a
length-prefixed tenant/project pair. Fixed-window state is isolated per scope,
backwards clock movement fails closed, expired scopes are pruned under bounded
capacity, and public throttle errors expose only a retry duration.

The race test concurrently drives:

- tenant A / shared project: 16 requests against limit 10;
- tenant B / the same textual project ID: 8 requests;
- tenant A / another project: 5 requests.

Only the first scope is throttled (10 allowed, 6 rejected), while both other
scopes remain independent. The error text contains no tenant/project identity.

Verification commands:

```text
go test -race ./test/pivot \
  -run '^TestCapability_RateLimitIsPerProjectAndDoesNotLeakCrossTenantState$' -count=1
go test ./...
go vet ./...
```

This is local concurrency evidence. Distributed multi-replica admission and
live OpenRouter/Apify/Bright Data calls remain provider/system gates.
