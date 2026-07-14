# PaaSApp standard-resource equivalence — 2026-07-14

Evidence class: `LOCAL_GREEN`

Requirement: `R13` — `TestRuntime_PaaSAppFastPathProducesEquivalentStandardResources`.

`PaaSApp` is now an adapter into an exported, provider-neutral standard
resource set: Deployment, Service, HPA, HTTPRoute and NetworkPolicy. The
operator reconciler consumes that exact rendered set rather than maintaining a
second privileged rendering path.

The executable contract test proves equality between the pure adapter output
and the resources written by reconciliation, including immutable image digest,
namespace, gVisor runtime class, disabled service-account token, restricted
security context, default-deny network policy and status observation.

Verification commands:

```text
go test ./internal/runtime/operator ./test/pivot \
  -run '^TestRuntime_PaaSAppFastPathProducesEquivalentStandardResources$' -count=1
go test -race ./internal/runtime/operator ./test/pivot \
  -run '^TestRuntime_PaaSAppFastPathProducesEquivalentStandardResources$' -count=1
go test ./...
go vet ./...
```

This is an operator contract gate; it does not claim a live Argo CD or
Kubernetes admission-policy gate.
