# Managed PostgreSQL workspace evidence — 2026-07-14

Evidence tier: `DB_GREEN`

## Bound revision

- Git revision: `9fb0b2d090c7758c089a13b11d7645753b1c01cb`
- Test image tag published by CI: `ai-native-paas-registry.registry.twcstorage.ru/ai-native-paas-tests:9fb0b2d090c7758c089a13b11d7645753b1c01cb`
- Resolved immutable image: `sha256:8bc27372b398a0d421045f4006b48bbd7afd19177b24a2511ef63f1f09ebdc36`
- Kubernetes Job: `ai-native-paas-system/iteration-5-control-plane-postgres-tests-9fb0b2d`

## Environment and scope

The full `make test-postgres` gate ran inside the admin Kubernetes cluster
against the Timeweb managed PostgreSQL instance over the private admin VPC.
Connection material was loaded only from the existing Kubernetes Secret and is
absent from the Job manifest, test logs, and this evidence.

This revision adds the durable workspace lifecycle and its second immutable
migration, persists Timeweb VM, disk, firewall, and cancellation identities,
and fixes serialization-failure precedence in the agent PostgreSQL adapter.
The gate covers clean and previous-schema migration, concurrent migration
startup, workspace lost-response recovery, JSONB array constraints, command
serialization, agent budget contention, and append-only audit/outbox behavior.

## Result

The Job reached `Complete=True` with one successful Pod and zero retries. The
workspace migration and lifecycle tests passed, including the stateful-command
single-winner check. The complete PostgreSQL suite also confirmed the agent
concurrent-budget regression no longer converts serialization failures into a
false permission denial.

## Reproduction

```sh
kubectl --kubeconfig infra/timeweb/ai-native-paas-test.kubeconfig \
  apply -f infra/timeweb/control-plane-postgres-test-job.yaml

kubectl --kubeconfig infra/timeweb/ai-native-paas-test.kubeconfig \
  -n ai-native-paas-system wait --for=condition=complete --timeout=15m \
  job/iteration-5-control-plane-postgres-tests-9fb0b2d
```

The checked-in Job is pinned to the resolved registry digest, not a mutable
tag. The Git SHA remains recorded separately so code and image provenance are
both explicit.
