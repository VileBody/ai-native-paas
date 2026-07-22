# Pre-beta credential rotation register

This register contains credential identifiers only. It must never contain a
credential value, digest of a credential value, or enough material to recover
one.

Development may continue with the credentials listed below. Promotion to
`v0.1.0-beta.1`, a beta availability window, or any tenant workload is blocked
until every `PENDING` row is rotated, all consumers are reconciled, the old
credential is proven rejected, and the secret sentinel passes.

| Credential / principal | Reason | Consumers to reconcile | Status |
|---|---|---|---|
| Timeweb API token `TWC_API_KEY_TRIAL_ACC` (`TWC_API_KEY_PULSE`) | exposed during operator diagnostics | local IaC/import environment and any operator automation using the same token | `PENDING` |
| Timeweb S3 user `ai-native-paas-workspace-logs` | active scoped pair exposed during operator diagnostics | workspace-manager Kubernetes Secret and operator files | `PENDING` |
| Timeweb S3 user `ai-native-paas-image-staging` | legacy sensitive remote-state output was rendered during workspace-image preparation | image-import operator files and scoped credential handoff | `PENDING` |

## Already closed

- The account-wide Timeweb S3 administrator secret was reset after scoped
  consumers were introduced; its previous pair is rejected.
- The state-service, Harbor and image/workspace-log consumers remain separate
  single-bucket principals. Their separation is not waived by this register.

## Release procedure

1. Rotate every `PENDING` credential at its provider.
2. Reconcile only the named consumers from owner-only local files or OpenBao.
3. Prove every old credential is rejected without printing either old or new
   material.
4. Scan Git, CI logs, OpenTofu plans/state output, Kubernetes Secrets, build
   layers, workspace logs, Argo objects and public responses with a sentinel.
5. Mark the row `CLOSED` with a link to dated evidence.
