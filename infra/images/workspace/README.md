# Disposable workspace VM image

The image is built from an exact Debian cloud artifact and an immutable Debian
snapshot. Every externally downloaded tool is versioned and checksum-pinned in
`images.lock.json`; the workspace-agent binary is built from the triggering Git
SHA. The result is a raw disk compressed with xz plus a manifest, SBOM and
keyless Sigstore bundle.

The runtime image has no SSH service or login user. The non-login
`workspace-agent` user owns `/workspace`, the durable command journal and the
short-lived mTLS bootstrap files. The agent has no listener. Its systemd unit
has a delegated cgroup subtree, and each command is born in its own cgroup so
timeout/cancel can use `cgroup.kill` even if a child calls `setsid()`.

Locked upstream versions are sourced from their official release channels:
OpenTofu 1.12.4, BuildKit 0.31.1, RootlessKit 3.0.2, Helm 3.21.3,
Kustomize 5.8.1, Cosign 3.1.1 and Syft 1.46.0. Updating any one of them is an
explicit lockfile change and produces a new disk digest.

Build on Linux with libguestfs/qemu installed:

```sh
./scripts/build-workspace-image.sh
```

The build does not import the image or change Timeweb state. After CI has
produced and signed an artifact, an operator imports the exact compressed
artifact with `scripts/import-timeweb-custom-image.py`, records the returned
Timeweb image ID and raw digest in `infra/stacks/workspace-images/images.lock.json`,
and applies that stack. Until both values are locked, production workspace
creation remains fail-closed.
