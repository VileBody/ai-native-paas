# Signed workspace VM image — 2026-07-22

Evidence tier: `LOCAL_GREEN` for the immutable VM artifact. Timeweb import and
boot remain live provider gates.

- Git revision: `041d1da39097218e40f7d2a8337074d7a06d3706`
- GitHub Actions workflow: `workspace-agent`
- Run: `29952874181`
- Artifact: `workspace-vm-041d1da39097218e40f7d2a8337074d7a06d3706`
- QCOW2 SHA-256:
  `f633a2042c0b7ed693d3496d8d03da76981812c1c8a680b4be5e2530d4dea1d3`
- Compressed image size: `697434112` bytes
- Virtual disk size: `42949672960` bytes

The workflow's verify, agent-image and VM-image jobs passed. The downloaded
artifact was independently checked against its `SHA256SUMS`; the manifest
binds the exact Git revision, QCOW2 digest, format and virtual size. The SPDX
SBOM is present and contains the pinned Git, OpenTofu, BuildKit, Helm,
Kustomize and Cosign components.

Both the QCOW2 and manifest Sigstore bundles passed `cosign verify-blob` with
the exact certificate identity
`https://github.com/VileBody/ai-native-paas/.github/workflows/workspace-agent.yml@refs/heads/codex/runtime-smoke-hello-go`
and issuer `https://token.actions.githubusercontent.com`.

The artifact has not been imported into Timeweb and no disposable VM was
created. The locally configured Timeweb API token and workspace-log scoped S3
credential were exposed during an operator diagnostic and must be rotated
before any live workspace action. This evidence does not substitute for the
private-VPC boot, mTLS enrollment, rootless BuildKit receipt, lease revocation
or VM destruction gates.
