# OpenBao 5/3 initialization and unseal ceremony

## Scope and stop condition

The Helm release deliberately stops at three TLS-protected, uninitialized OpenBao
servers. Initialization is a human custody ceremony, not a CI step. Never place the
five unseal shares or the initial root token in Git, Terraform/OpenTofu state,
Kubernetes Secrets, CI artifacts, chat, or a shared password vault entry.

The beta gate requires five named holders and at least three independent custody
locations. If fewer than three holders are present, leave OpenBao uninitialized.

## Preconditions

- `openbao-0`, `openbao-1`, and `openbao-2` are Running on distinct system nodes.
- Every server PVC and audit PVC is Bound using the Timeweb NVMe StorageClass.
- The TLS certificate verifies against `.operator/openbao/ca.crt`.
- Five operators have agreed on secure, independent storage for one share each.
- A sixth temporary operator records only the root-token rotation checklist, never
  the token itself.

## Initialize

Run from an operator terminal with local command history disabled:

```bash
set +o history
KUBECONFIG=infra/timeweb/ai-native-paas-test.kubeconfig
kubectl --kubeconfig "$KUBECONFIG" -n openbao exec -it openbao-0 -- \
  bao operator init -key-shares=5 -key-threshold=3
```

Each share is handed directly to exactly one holder. The initial root token is used
only for bootstrap and is revoked after Kubernetes auth, audit devices, policies and
operator identities are configured.

## Unseal and join

Three different holders enter their share into `openbao-0`. After it becomes active,
repeat with three holders for `openbao-1` and `openbao-2`. Never pass shares as command
arguments or environment variables; use the interactive prompt.

Verify membership without exposing credentials in process arguments:

```bash
kubectl --kubeconfig "$KUBECONFIG" -n openbao exec -it openbao-0 -- bao status
kubectl --kubeconfig "$KUBECONFIG" -n openbao exec -it openbao-0 -- bao operator raft list-peers
```

Expected result: one leader, two voters, all three nodes unsealed.

## Immediate bootstrap

1. Enable a file audit device at `/openbao/audit/audit.log` and verify a record lands
   on the active server's dedicated audit PVC.
2. Enable Kubernetes auth and bind only explicit service accounts and namespaces.
3. Enable KV v2 at `platform/`; deny raw reads to platform APIs that only need
   metadata or write-only operations.
4. Create a short-lived snapshot policy and Kubernetes-auth role for the snapshot
   CronJob. Never give it the initial root token.
5. Create named human operator identities, test login, then revoke the initial root
   token.
6. Record holder names, share fingerprints and custody acknowledgements in the
   offline operator log. Do not record share contents.

## Failover gate

Delete the active pod, confirm a standby becomes active, then unseal the recreated
pod with three holders. Run a KV write/read-metadata/delete sentinel before and after
the leader change. Finally take a Raft snapshot, restore it into an isolated namespace
and prove the sentinel metadata is present without exposing its value.
