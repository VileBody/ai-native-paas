# Helm runtime allocation in cost estimates — 2026-07-14

Evidence class: `LOCAL_GREEN`.

Requirement: `C3` —
`TestCost_HelmResourcesContributeRequestedRuntimeAllocation`.

The infrastructure plan application now accepts bounded rendered Kubernetes
manifests and derives a value-free `RequestedRuntimeAllocation`. The canonical
plan hash and estimate version bind:

- CPU requests in millicores multiplied by workload replicas;
- memory requests in MiB multiplied by workload replicas;
- requested PersistentVolumeClaim storage in MiB;
- the number of `LoadBalancer` Services.

Container limits are not read as guaranteed capacity. Kubernetes init-container
maxima and pod overhead are included in effective requests. Invalid quantities,
duplicate resources, arithmetic overflow and more than 4,096 rendered resources
fail closed. DaemonSets and CronJobs with requests require explicit cardinality
or schedule policy instead of silently producing an underestimate.

Pricing remains separate in the immutable rate card. Each normalized quantity
is multiplied by its unit provider price with checked integer arithmetic and the
project markup is then applied. Missing runtime prices produce the existing
conservative unknown-price range and approval requirement.

The executable test uses a three-replica Deployment with `250m` CPU and `128Mi`
memory requests, much larger limits, a limits-only sidecar, a `10Gi` PVC and one
load balancer. It proves the estimate contains exactly 750 millicores, 384 MiB,
10,240 MiB and one load balancer, and that the allocation changes both plan and
estimate identity.

Verification:

```text
go test -count=1 ./test/pivot \
  -run '^TestCost_HelmResourcesContributeRequestedRuntimeAllocation$' -v
go test -count=1 ./...
go test -race -count=1 ./...
go vet ./...
make fmt-check generate-check
```

Matrix result:

- 157 requirements;
- 991 discovered Go test/fuzz targets;
- `REUSED 103`, `NEW 0`, `LIVE_ONLY 54`;
- zero unmapped requirements;
- C3 now maps to executable rendered-runtime allocation evidence.

The trusted Helm/Kustomize render receipt still needs to be connected to this
application input in the end-to-end GitOps orchestration slice; this local gate
does not claim a live Cozystack or provider price-catalog result.
