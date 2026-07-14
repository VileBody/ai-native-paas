# Health repair through Git evidence — 2026-07-14

Evidence class: `LOCAL_GREEN`

Requirement: `G19` —
`TestAgent_HealthFailureCreatesNewPatchCommitNotDirectClusterFix`.

The acceptance flow now proves that a failed runtime health observation is
repaired through source, build and deployment identities rather than a direct
cluster mutation.

Observed invariants:

- the initial source patch produces a deterministic commit identity and the
  build identity is bound to that exact commit;
- the first deployment is observed as `FAILED` with zero ready replicas;
- reading the failed health status does not call the runtime reconciler or
  mutate the deployment;
- repair creates a second patch on the initial commit, a distinct commit SHA,
  a build bound to that SHA and a distinct immutable artifact digest;
- the repair deploy uses the next expected environment revision and creates a
  new release/deployment while the failed deployment remains historical;
- `Runtime.ReconcileCalls` remains zero throughout the repair; only the normal
  GitOps deploy port receives the new artifact;
- audit contains two source patches, two builds, two deploys and both health
  observations under the same task/correlation identity.

Verification commands:

```text
go test ./test/acceptance \
  -run '^TestAgent_HealthFailureCreatesNewPatchCommitNotDirectClusterFix$' \
  -count=1 -v
go test ./...
go vet ./...
go test -race ./internal/agent/application ./internal/agent/testkit \
  ./test/acceptance
./scripts/generate-pivot-tdd-matrix.py --check
```

This evidence uses provider-neutral acceptance fakes. It does not claim a live
Argo health event, real GitLab commit, Harbor artifact or Cozystack rollout;
those remain provider/system gates.
