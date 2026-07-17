# OpenBao workspace-manager Kubernetes-auth gate — 2026-07-17

Target: `ai-native-paas-test`, `workspace-manager` ServiceAccount in
`ai-native-paas-system`.

## Live policy boundary

The existing `auth/kubernetes/role/workspace-manager` was verified against the
live OpenBao API. It is bound only to the `workspace-manager` ServiceAccount in
`ai-native-paas-system`, receives only the `workspace-manager` policy, has a
15-minute TTL and a 30-minute maximum TTL.

The policy grants only:

- `update` for the exact workspace-agent PKI issue/sign paths;
- `read` for command-scoped `workspace-credentials` envelopes; and
- lease revocation.

It grants no Transit signing capability.

## Reproducible live check

`scripts/sync-openbao-client-ca.sh` copies only the public server CA to the
control-plane namespace. It never copies the TLS private key, operator token or
Shamir material.

`scripts/verify-openbao-workspace-manager-auth.sh` creates a restricted,
one-shot Job with that ServiceAccount. The Job authenticates with its projected
Kubernetes token, stores the returned OpenBao token only in its own temporary
filesystem, checks the expected capabilities and Transit denial, revokes the
token and is deleted by the caller. No certificate or credential envelope is
issued and no token is printed.

The role verifies `audience=openbao`, so the workload and gate use a
15-minute projected token at the standard ServiceAccount path. The named volume
is also passed to the Agent Injector, preventing a second default-audience token
from being mounted into the injected agent.

`scripts/verify-openbao-workspace-manager-injector.sh` uses those exact
annotations with pre-populate-only injection. It verifies only that the
Injector performs the Kubernetes login and places a mode-restricted token file
in its memory volume; it never reads the token or requests a secret.

This is an identity-scope gate. Deploying `workspace-manager`, issuing a real
workspace certificate, provisioning a disposable VM and granting a separate
build signing role remain independent release work.
