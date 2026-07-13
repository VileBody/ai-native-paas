# ADR 0002: Separate admin infrastructure from user infrastructure

- Status: accepted
- Date: 2026-07-14

## Decision

All beta resources remain owned by the Timeweb Cloud project `BALOVSTVO`, but
they are separated by cluster, network, identity and lifecycle boundary.

- Admin infrastructure uses the managed Kubernetes cluster and managed
  PostgreSQL for identities, operations, approvals, subscriptions and usage.
- User workloads and managed resources use a separate self-managed
  Talos/Cozystack runtime cell.
- Disposable workspaces use a third VPC and cannot route to either admin or
  runtime control-plane networks.
- Provider and runtime bridges establish outbound mTLS sessions. Workspaces do
  not receive administrator kubeconfigs or provider master keys.

`BALOVSTVO` is the intentional billing/ownership project. Moving these
resources to another Timeweb project is not backlog work.
