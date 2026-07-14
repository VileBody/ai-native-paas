# Disposable workspace VM image

The image is built from an exact Debian cloud artifact and an immutable Debian
snapshot. Every externally downloaded tool is versioned and checksum-pinned in
`images.lock.json`; the workspace-agent binary is built from the triggering Git
SHA. The result is a raw disk compressed with xz plus a manifest, SBOM and
keyless Sigstore bundle.

The runtime image has no SSH service or login user. A hardened supervisor owns
the durable journal and identity files. Ordinary commands and rootless
BuildKit run as `workspace-task`; governed operations run as the separate
`workspace-verified` UID and receive the short-lived identity only through
command-scoped file descriptors (the key is attached only for commit/plan
receipt operations). Those FDs are marked close-on-exec before
Git/OpenTofu children start, and neither command identity can open the identity
directory. The non-login identities share `/workspace` through the
non-identity `workspace-shared` group. The agent has no listener.
Its systemd unit has a delegated cgroup subtree, and each command is born in
its own cgroup so timeout/cancel can use `cgroup.kill` even if a child calls
`setsid()`.

The only listener is an unprivileged proxy bound to `127.0.0.1:18081`. Both
agent commands and rootless BuildKit use it; every outbound HTTPS stream is
then carried through the mTLS workspace egress gateway. The Timeweb firewall
does not permit a direct Internet fallback.

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
