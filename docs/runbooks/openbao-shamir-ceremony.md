# OpenBao 5/3 initialization and unseal ceremony

## Scope and stop condition

The Helm release deliberately stops at three TLS-protected, uninitialized OpenBao
servers. Initialization is a human custody ceremony, not a CI step. Never place the
five unseal shares or the initial root token in Git, Terraform/OpenTofu state,
Kubernetes Secrets, CI artifacts, chat, or a shared password vault entry.

The beta gate requires three named holders and three independent custody
destinations. Distribute the five shares as `2/2/1`: holder A receives shares
1 and 2, holder B receives shares 3 and 4, and holder C receives share 5. No
single holder reaches the threshold of three. If fewer than three holders and
destinations are confirmed, leave OpenBao uninitialized.

## Explicit solo-development exception

The owner may explicitly authorize `solo-dev` custody for a non-beta development
window. In that mode one human controls the ceremony, but the `2/2/1` share sets
must still be separately encrypted across three destinations and use separate
wrapping keys. The custody log and evidence must say `solo-dev-not-beta-custody`.
Never count this as the independent-holder beta gate, never invite tenants while
it is active, and perform a controlled rekey plus redistribution to three named
holders before a controlled-beta release.

## Preconditions

- `openbao-0`, `openbao-1`, and `openbao-2` are Running on distinct system nodes.
- Every server PVC and audit PVC is Bound using the Timeweb NVMe StorageClass.
- The TLS certificate verifies against `.operator/openbao/ca.crt`.
- Three holders have agreed on secure, independent storage with the `2/2/1`
  allocation recorded in the offline custody log.
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

Each share is handed directly to its assigned holder; a holder's two shares must
remain separately wrapped and labelled by fingerprint. The initial root token is used
only for bootstrap and is revoked after Kubernetes auth, audit devices, policies and
operator identities are configured.

## Unseal and join

At least two holders jointly enter three shares into `openbao-0`; prefer all
three holders for the initial ceremony. After it becomes active, repeat for
`openbao-1` and `openbao-2`. Never pass shares as command arguments or
environment variables; use the interactive prompt.

Verify membership without exposing credentials in process arguments:

```bash
kubectl --kubeconfig "$KUBECONFIG" -n openbao exec -it openbao-0 -- bao status
kubectl --kubeconfig "$KUBECONFIG" -n openbao exec -it openbao-0 -- bao operator raft list-peers
```

Expected result: one leader, two voters, all three nodes unsealed.

## Immediate bootstrap

1. Verify the declarative file audit device from the Helm server configuration is
   active at `/openbao/audit/audit.log` and a record lands on the active server's
   dedicated audit PVC. OpenBao 2.4+ intentionally disables API-driven file audit
   creation by default; never enable the unsafe compatibility switch.
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
