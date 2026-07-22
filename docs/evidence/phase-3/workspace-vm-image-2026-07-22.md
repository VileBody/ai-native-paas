# Signed workspace VM image — 2026-07-22

Evidence tier: `PROVIDER_GREEN` for the immutable VM artifact and Timeweb image
import. Private-VPC boot remains a live provider gate.

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

The verified QCOW2 was staged with the image-staging single-bucket S3 principal,
imported as Timeweb custom image
`803212b3-aa74-4c13-9bdf-77b864216994`, and reached `created` at 100%. The
staging objects were removed after import. The `workspace-images` remote state
now binds that image ID to the exact `sha256:f633...a1d3` digest and retains no
S3 credential outputs. Its workspace network outputs point to VPC
`network-687cddd36c3147b3bff75c79e9779498`, shared router
`49de7bfa-90b5-4ca5-b94a-b7d6aa7a1de4`, and NAT `72.56.234.22`.

No disposable VM has booted from the image yet. The active development
provider and scoped staging/log credentials are recorded by identifier in
`verification/PRE_BETA_CREDENTIAL_ROTATION.md`; development gates may continue,
but beta promotion is blocked until their final rotation and rejection proof.
This evidence does not substitute for private-VPC boot, mTLS enrollment,
rootless BuildKit receipt, lease revocation or VM destruction gates.
