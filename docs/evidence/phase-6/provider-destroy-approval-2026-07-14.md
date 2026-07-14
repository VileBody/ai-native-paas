# Provider destroy approval evidence — 2026-07-14

Evidence class: `LOCAL_GREEN`

Requirement: `A5.19` — `TestProviderResource_DestroyRequiresMatchingPlanHashApproval`.

The provider-resource purge path now builds a canonical `sha256:` destructive
plan from the exact tenant, internal and external resource identities, service
plan version, provider mapping version, and backup policy. The approval port is
bound to that hash, target and actor before any backup or delete side effect.

The executable test proves that:

- approval for plan hash A cannot authorize plan hash B;
- approval for another target cannot authorize the current resource;
- the provider delete adapter is not called for either mismatch;
- the exact binding authorizes one delete.

Verification commands:

```text
go test ./test/pivot -run '^TestProviderResource_DestroyRequiresMatchingPlanHashApproval$' -count=1
go test -race ./test/pivot -run '^TestProviderResource_DestroyRequiresMatchingPlanHashApproval$' -count=1
go test ./internal/attachments/application ./test/pivot
go test ./...
go vet ./...
python3 scripts/generate-pivot-tdd-matrix.py --check
```

This is deterministic governance evidence. It does not claim a live Cozystack
destroy or backup gate.
