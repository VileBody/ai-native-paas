# OpenBao HA, custody, snapshot and restore gate — 2026-07-16

Target: `ai-native-paas-test`, namespace `openbao`.

## Custody mode

OpenBao was initialized with Shamir `5/3`. This ceremony deliberately used the
temporary `solo-dev` custody profile requested by the owner; it is **not** the
three-independent-holder custody required for controlled beta. The five shares
were split `2/2/1` across separately encrypted local, versioned S3 and iCloud
destinations. Their wrapping keys are separate macOS Keychain items. No share,
root token or operator password is stored in Git or Kubernetes.

Safe share fingerprints, used only to identify the encrypted artifacts:

```text
1 22de41171201e965
2 40de5076bcfec721
3 a8481fa9db0067f8
4 e255ff885c0e9e66
5 cce3d503da2369a1
```

The initialization root token was revoked. A named `ergin` userpass operator
was created for the solo-development window and its credential is held in
Keychain. This broad operator policy must be replaced by independent beta
operator custody before release.

## Security configuration

- Kubernetes auth and scoped roles were enabled for platform workloads.
- `platform/` and `workspace-credentials/` KV v2 mounts were created.
- The workspace PKI role issues 15-minute workspace-agent certificates.
- The workspace-manager Kubernetes login returned a 900-second token with only
  the expected issue/sign/revoke capabilities.
- The snapshot service account returned a 900-second scoped token with Raft
  snapshot read capability.
- File audit is configured declaratively at `/openbao/audit/audit.log` on the
  dedicated retained audit PVC; OpenBao 2.5 does not allow creating this audit
  backend through the API.

## HA and restore evidence

The initial leader `openbao-0` was deleted. Leadership moved to `openbao-1`, a
non-secret sentinel remained readable, and the recreated `openbao-0` was
unsealed from encrypted custody and rejoined. The resulting cluster had three
healthy voting peers.

An encrypted Raft snapshot was restored in an isolated temporary namespace.
The restore test used the original Shamir seal, performed documented one-node
Raft peer recovery, unsealed with three encrypted shares, verified the sentinel,
and expanded back to three healthy voters. The temporary namespace and every
plaintext snapshot copy were removed after the gate.

The final post-cleanup snapshot taken immediately before admin `off` is:

```text
s3://ai-native-paas-state/admin-backups/openbao/final-before-admin-off/20260715T213756Z/openbao-raft.snap.enc
S3 version: hYS7UIFYUifgJUX0pYGyyFYI.poL.Zi
SHA-256: aba25a42ee269875cf05ffe5408d5ea3705e707e41e3b3717b86fa72b55bf292
```

The snapshot token had `num_uses=1`; the snapshot consumed it and a second use
was rejected. The encrypted local copy is under ignored owner-only
`.operator/admin-backups/` custody. The decryption key is a separate Keychain
item. The production sentinel and temporary operator token were removed before
this final snapshot.

Result: initialization, scoped auth, audit, leader failover, peer rejoin,
encrypted snapshot, isolated restore and three-peer reconstruction are green.
Controlled rekey and redistribution to three independent human holders remains
a beta release gate.
