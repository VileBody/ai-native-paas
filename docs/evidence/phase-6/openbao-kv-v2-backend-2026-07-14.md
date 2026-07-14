# OpenBao KV v2 attachment backend — 2026-07-14

Evidence class: `LOCAL_GREEN`.

The Attachments secret port now has a production-oriented OpenBao KV v2 HTTP
backend. It accepts only HTTPS endpoints (loopback HTTP is test-only), reloads
the Kubernetes-injected workload token for every request, limits values and
responses, and never includes provider response bodies, tokens or secret values
in returned errors.

The implementation follows the OpenBao KV v2 API distinction between a soft
delete at `DELETE /data/:path` and permanent removal of all versions at
`DELETE /metadata/:path`. Attachment deletion uses the metadata endpoint so an
old value cannot be undeleted. See the official
[OpenBao KV v2 API](https://openbao.org/api-docs/2.3.x/secret/kv/kv-v2/).

Validated invariants:

- binary secret values are base64-encoded inside the KV v2 `data` envelope;
- metadata exposes only the current version/existence state;
- deletion is idempotent and permanently removes every stored version;
- `429`/`5xx` and transport failure are retryable without exposing provider
  bodies;
- rotated injector tokens are read on the next request;
- token files support Kubernetes `fsGroup` modes `0440`/`0640`, while execute,
  group-write and world permissions are rejected;
- non-canonical paths, userinfo URLs, non-loopback plaintext HTTP and invalid
  mounts are rejected;
- KV v2 does not claim atomic prefix deletion. Wildcard credential revoke fails
  closed and must later use a lease-aware credential engine.

Canonical executable evidence:

- `TestKVV2Backend_WriteMetadataAndPermanentlyDeleteAllVersions`;
- `TestKVV2Backend_ReloadsRotatedWorkloadTokenAndContainsProviderErrors`;
- `TestKVV2Backend_RejectsUnsafeTransportPathAndTokenFile`;
- `TestCredential_RevokePrefixFailsClosedWhenBackendCannotGuaranteeIt`;
- `TestOpenBaoWorkloadTokenSupportsKubernetesFSGroupWithoutWorldAccess`.

Verification:

```text
go test -count=1 ./...
go test -race -count=1 ./...
go vet ./...
make fmt-check generate-check
```

Matrix result:

- 157 requirements;
- 979 discovered Go test/fuzz targets;
- `REUSED 92`, `NEW 0`, `LIVE_ONLY 65`;
- zero unmapped requirements;
- `A5.6` is now mapped to executable fail-closed production-adapter evidence.

This is not `PROVIDER_GREEN`: the admin OpenBao release remains intentionally
sealed and uninitialized pending the Shamir 5/3 ceremony. The backend is not
yet wired into `attachments-api`; that entrypoint also requires production
environment, Cozystack, approval, runtime and commerce gateways before its
production profile may start.
