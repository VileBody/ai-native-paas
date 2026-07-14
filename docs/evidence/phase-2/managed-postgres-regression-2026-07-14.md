# Managed PostgreSQL regression evidence — 2026-07-14

Evidence tier: `DB_GREEN`

## Bound revision

- Git revision: `78ce2f87b1f785e57518ca746d28a3d8316d57bc`
- Test image reference: `ai-native-paas-registry.registry.twcstorage.ru/ai-native-paas-tests:78ce2f87b1f785e57518ca746d28a3d8316d57bc`
- Resolved immutable image: `sha256:93dc75410cfa35861ce0b7f8f2194b15d845b87aff26a7b641206530d784aaa8`
- Kubernetes Job: `ai-native-paas-system/iteration-5-control-plane-postgres-tests-78ce2f8`

## Environment and result

The full `make test-postgres` gate ran inside the existing admin Kubernetes
cluster against the Timeweb managed PostgreSQL instance through its private
VPC address. Database connection material was loaded only from the Kubernetes
Secret and was not printed into the Job manifest, logs, or this evidence.

The Job reached `Complete=True`. This independently confirms the same revision
that passed all six GitHub Actions workflows, including the previously flaky
agent concurrent-budget and runtime stale-version paths.

## Reproduction

```sh
kubectl --kubeconfig infra/timeweb/ai-native-paas-test.kubeconfig \
  apply -f infra/timeweb/control-plane-postgres-test-job.yaml

kubectl --kubeconfig infra/timeweb/ai-native-paas-test.kubeconfig \
  -n ai-native-paas-system wait --for=condition=complete \
  job/iteration-5-control-plane-postgres-tests-78ce2f8
```

The checked-in Job uses the exact commit tag and `IfNotPresent`; it no longer
uses the historical mutable `iteration-5` image tag.

