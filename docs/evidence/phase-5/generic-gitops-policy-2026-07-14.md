# Generic GitOps path and rendered-resource policy — 2026-07-14

Evidence class: `LOCAL_GREEN`, `SECURITY_GREEN`.

Requirements:

- `R1` — `TestGitOps_CommitMayModifyOnlyProjectEnvironmentPath`;
- `R4` — `TestGitOps_ForbiddenClusterScopedResourceRejectedBeforeCommit`;
- `R5` — `TestGitOps_PrivilegedWorkloadRejectedRegardlessOfHelmSource`.

The generic delivery boundary now validates an entire change set against the
verified cell, tenant, project and environment path before calling the Git
committer once. A mixed change set containing one valid file and one foreign
cell/project/environment or platform-system path is rejected with zero commit
calls, so validation cannot produce a partial commit.

Rendered Kubernetes YAML is evaluated independently of Helm/Kustomize/plain
source provenance and independently of the trusted-recipe marker. The policy:

- requires every namespaced object to target the verified namespace;
- rejects cluster-scoped resources such as ClusterRole, CRD, Node and
  StorageClass unless apiVersion, kind and name match an exact platform grant;
- rejects Pod and controller templates using privileged execution,
  privilege escalation, hostNetwork/hostPID/hostIPC, hostPath or Windows
  hostProcess;
- records a policy version and manifest digest without executing a renderer or
  contacting Argo.

Verification:

```text
go test -count=1 ./internal/runtime/gitops
go test -count=1 ./...
go test -race -count=1 ./...
go vet ./...
make fmt-check generate-check
```

Matrix result:

- 157 requirements;
- 987 discovered Go test/fuzz targets;
- `REUSED 99`, `NEW 0`, `LIVE_ONLY 58`;
- zero unmapped requirements;
- R1, R4 and R5 now map to executable application/policy evidence.

This is the pre-commit policy boundary. A real Argo AppProject admission/sync
test remains R6 and is intentionally still a live Kubernetes gate.
