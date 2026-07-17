# ADR 0006: Quarantine operator-owned legacy workloads in the admin cluster

- Status: accepted
- Date: 2026-07-15

## Context

The `call-analytics-k8s` cluster contains a small set of still-needed,
operator-owned applications. Keeping an otherwise redundant Kubernetes cluster
for those applications is disproportionately expensive during development.

These applications are not PaaS tenant workloads. They predate the platform,
are not provisioned by the execution kernel, and must never be discovered,
reconciled, metered, or billed by the PaaS runtime controllers.

## Decision

The following legacy applications may run in the managed admin cluster as a
temporary, explicit exception to ADR 0002:

- `rec-sidecar`, including `rec.teamgenius.ru`, `suffleur.teamgenius.ru`, and
  `klukhanova.teamgenius.ru`;
- `vietnam-rent`, including `vietnam.teamgenius.ru`.

The exception has these boundaries:

- workloads live only in namespaces labelled
  `ai-native-paas.io/scope=legacy-quarantine`;
- workloads select the existing `ai-native-paas.io/pool=ci` worker and do not
  tolerate the `ai-native-paas.io/system=true:NoSchedule` taint;
- GitOps is provided by a headless Argo CD Core installation in
  `legacy-argocd`;
- Argo CD uses exact Git commit SHAs, and running images are pinned to the
  digests captured during migration;
- the Cozystack controllers, platform runtime bridge, usage collector, and
  billing code must ignore every namespace with the legacy scope label;
- legacy PostgreSQL, NATS, and Tempo remain namespace-local and are not shared
  with admin or tenant services;
- public ingress reuses the retained Timeweb load balancer address. The
  migration must not delete any public IPv4 resource from the old cluster.

`call-analytics`, its analytics dashboard, and
`dental-practice-assistant` are intentionally not migrated.

## Exit

Legacy workloads can later move to their own destination or be reconstructed
from Git. Removing this exception requires deleting the three legacy
namespaces and the corresponding load-balancer backends; it does not authorize
deleting any retained public IPv4 address.
