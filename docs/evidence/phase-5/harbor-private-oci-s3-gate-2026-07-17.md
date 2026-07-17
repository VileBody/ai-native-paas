# Harbor private OCI/S3 and scoped-robot gate — 2026-07-17

Target: `ai-native-paas-test`, namespace `harbor-system`, development capacity
mode (one private system worker).

## Live result

The internal `ai-native-paas-harbor` Helm release is deployed as revision 3.
All six workload pods were Ready and the in-cluster HTTPS health endpoint
returned `200`. The service remains a private `ClusterIP`; no public Harbor
ingress was created.

The executable gate is:

```text
./scripts/harbor-live-oci-gate.sh
```

Its final output was:

```text
Harbor private OCI/S3 round trip and robot revocation passed
```

After the run, the corresponding temporary Job and Pod were absent. The
unrelated legacy VietNest health endpoint remained `{"ok":true}`.

## What the gate proves

The gate runs in a restricted-Pod-Security Job on the system worker. It receives
the existing Harbor admin credential only from the namespace-local Secret; it
does not print or persist it, the generated project robot secret, or either
registry JWT.

For a unique private project, it verifies this live sequence:

```text
create project
-> create project-scoped pull/push robot
-> issue registry token
-> upload OCI config blob
-> publish OCI manifest
-> read manifest and blob back
-> delete robot
-> issue a fresh token with the deleted robot credential
-> prove that token cannot read the artifact
-> delete repository and project
```

The config blob and manifest round trip use Harbor's dedicated private Timeweb
S3 bucket, not a local registry volume. The authorization test deliberately
checks an actual manifest read after revocation: the token endpoint can return
`200` with an unusable scope, so its HTTP status alone is not treated as
revocation evidence.

## Explicit limits

This is an internal Harbor storage and scoped-credential gate, not the final
verified-build release gate. Application code does not yet obtain production
robot leases from OpenBao, no disposable workspace receipt is ingested, Trivy
is disabled in the development profile, and the chart component images still
use upstream version tags rather than immutable deployment digests. Those
requirements remain release blockers.
