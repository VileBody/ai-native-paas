# Workspace command identity separation — 2026-07-14

Status: `LOCAL_GREEN`; immutable-image and live-VM security gates remain
pending.

Implemented boundary:

- The workspace supervisor is the only root process. Its systemd capability
  bounding set contains only `CAP_SETUID` and `CAP_SETGID`; ambient
  capabilities are empty and `NoNewPrivileges=yes` remains enabled.
- Ordinary commands and rootless BuildKit are changed to the non-login
  `workspace-task` UID/GID before `exec`; governed Git and OpenTofu operations
  use a distinct non-login `workspace-verified` UID/GID. This also removes the
  pre-hardening same-UID observation window between task/BuildKit processes
  and a newly started verified process.
- The supervisor's primary `workspace-agent` group can read the root-owned
  `0440` bootstrap identity in a root-owned `2750` directory. Both command
  identities replace that primary group and retain only the non-identity
  `workspace-shared` supplementary group used for `/workspace`.
- Ordinary commands receive no identity files or paths. Governed operations
  receive an unlinked, rewritten configuration as FD 3; only commit receipt
  signing and authenticated plan-receipt submission additionally receive the
  exact certificate, private-key and CA files as FDs 4–6.
- At verified-subcommand startup those FDs are marked close-on-exec, the
  process is made non-dumpable, and `no_new_privs` is asserted before any
  repository-controlled Git/OpenTofu/provider child can start. Descendants
  therefore cannot inherit the key or inspect the key-bearing process.
- Workspace-manager accepts the `workspace-agent` executable only when the
  persisted command kind maps to the exact governed subcommand. Generic or
  mismatched command kinds fail closed before dispatch.
- Certificate rotation preserves `root:workspace-agent` group-read semantics;
  group write/execute and every other-user permission remain rejected.

Local evidence:

- `go test ./...` passed.
- Workspace-agent, workspace service, bootstrap and architecture race suites
  passed.
- Linux workspace-agent tests and the production command cross-compile for
  `linux/amd64`; the image build script passes shell syntax validation.
- Architecture tests pin the supervisor capability set, task BuildKit user,
  identity directory mode and shared workspace ownership.
- The Linux-only delegated FD layout, `FD_CLOEXEC`, non-dumpable and
  `no_new_privs` test executed successfully from the static test binary in a
  temporary admin-cluster pod. The pod carried no platform secret or volume
  and was deleted immediately after the test; deletion was confirmed through
  the Kubernetes API.

Required live evidence before `SECURITY_GREEN`:

- boot the newly signed immutable workspace image and inspect actual systemd
  effective/permitted capabilities and supplementary groups;
- execute generic, verified Git and OpenTofu/provider-plugin sentinel probes
  proving that `/var/lib/ai-native-paas/identity` and FDs 3–6 are unavailable
  to descendants;
- repeat timeout/cgroup, credential redaction and certificate rotation gates
  on that VM, then destroy the VM and disk.
