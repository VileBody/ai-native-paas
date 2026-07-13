# ADR 0003: Use disposable Timeweb VMs for beta workspaces

- Status: accepted
- Date: 2026-07-14

## Decision

The controlled beta executes untrusted Git, OpenTofu, BuildKit, Helm,
Kustomize and controlled shell commands in one disposable Timeweb VM per task.
Kubernetes Jobs and control-plane host processes are not valid execution
backends for the production profile.

Each VM boots a pinned workspace image, establishes an outbound mTLS session,
receives only project- and command-scoped leases, and is destroyed after task
completion, failure or TTL expiry. Logs and explicitly uploaded artifacts are
the only retained workspace outputs.

Cozystack KubeVirt stays disabled until Timeweb confirms nested virtualization
and host CPU passthrough. This does not block the VM-backed beta workspace
contract.
