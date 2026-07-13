# Iteration 3 handoff to Runtime Delivery

## Machine status

**PASS — local composite release gate.**

The long single-process verification script was interrupted by the execution host's command-duration limit during a race stage. Every gate was then rerun independently and passed; the packaged verification log identifies the composite execution explicitly.

## Runtime Delivery may consume

```go
pkg/contracts/build/v1.ArtifactRef
pkg/contracts/build/v1.ArtifactPolicy
pkg/contracts/build/v1.ReleasabilityDecision
```

The release workflow should:

1. receive an `ArtifactRef`;
2. call `ArtifactPolicy.IsReleasable`;
3. reject any non-allowed decision;
4. persist the exact `repository@digest`;
5. never resolve a mutable tag;
6. promote the same digest across environments without rebuilding.

## Runtime Delivery must not

- read schema `build` directly;
- import `internal/build/...`;
- query GitLab or resolve branch heads;
- accept an image tag as release identity;
- bypass the policy decision because Argo or Kubernetes can pull the image;
- mutate scan, signature, SBOM, or artifact records;
- make deployment success imply build trust.

## Iteration ownership

Any later defect involving source checkout, runtime detection, build identity, build state, cache isolation, registry digest, SBOM, scan, signature, or Build PostgreSQL semantics belongs to an Iteration 3 patch.

Any Source Control defect remains an Iteration 2 patch. The compatibility repairs discovered here are documented separately.
