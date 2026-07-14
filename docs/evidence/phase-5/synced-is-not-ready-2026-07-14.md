# Argo synced is not workload ready — 2026-07-14

Evidence class: `LOCAL_GREEN`.

Requirement: `R7` —
`TestRuntime_ArgoSyncedWithoutHealthyWorkloadsIsNotReady`.

The existing runtime status reconciler already required an observed current
generation and an operator `Ready` phase before activation, but its executable
test was not discoverable under the canonical pivot requirement name.

The canonical test now proves that an immutable runtime object present at the
expected generation (the Argo-synced condition) with `Deploying` status and zero
ready replicas:

- remains `ROLLING_OUT` rather than `READY`;
- does not activate the candidate release;
- does not set the environment active release;
- does not publish an endpoint or ready replica count.

Verification:

```text
go test -count=1 ./internal/runtime/application \
  -run '^TestRuntime_ArgoSyncedWithoutHealthyWorkloadsIsNotReady$' -v
go test -count=1 ./...
go test -race -count=1 ./...
go vet ./...
make fmt-check generate-check
```

Matrix result:

- 157 requirements;
- 989 discovered Go test/fuzz targets;
- `REUSED 101`, `NEW 0`, `LIVE_ONLY 56`;
- zero unmapped requirements;
- R7 now maps to the existing application/system readiness boundary.

This local evidence covers control-plane aggregation. Live Argo and Kubernetes
health observation remains part of the runtime system gate.
