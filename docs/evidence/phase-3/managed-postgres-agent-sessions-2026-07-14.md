# Managed PostgreSQL workspace-agent session evidence — 2026-07-14

Evidence tier: `DB_GREEN`

## Bound revision

- Git revision: `797726160f28d79d7a5bd5bfb185058fba539932`
- CI image tag: `ai-native-paas-registry.registry.twcstorage.ru/ai-native-paas-tests:797726160f28d79d7a5bd5bfb185058fba539932`
- Resolved immutable image: `sha256:4b6c4144e48544d12adc0d5bb16c7f8366e03830b50028628ed763b24351ce4d`
- Kubernetes Job: `ai-native-paas-system/iteration-5-control-plane-postgres-tests-7977261`

## Scope

This revision adds the third immutable workspace migration and the durable
outbound workspace-agent channel. PostgreSQL owns certificate-bound sessions,
idempotent command messages, delivery leases, redelivery attempts, and durable
ACK/rejection state. The integration test disconnects the control-plane
registry after ACK and proves a restarted registry returns the original
receipt without creating a second message or repeating execution.

The full `make test-postgres` gate runs in the admin Kubernetes cluster against
Timeweb managed PostgreSQL through its private VPC endpoint. Database
credentials remain in the existing Kubernetes Secret and are not serialized
into this file, the Job manifest, or test output.

## Result

The Job reached `Complete=True` with one successful Pod and zero retries.
`TestPostgres_WorkspaceAgentAckSurvivesControlPlaneRestart` passed against the
managed database, as did the complete PostgreSQL regression suite. This proves
the clean migration, foreign-key boundary, durable ACK replay, and single
message identity on the target admin data plane.

## Reproduction

```sh
kubectl --kubeconfig infra/timeweb/ai-native-paas-test.kubeconfig \
  apply -f infra/timeweb/control-plane-postgres-test-job.yaml

kubectl --kubeconfig infra/timeweb/ai-native-paas-test.kubeconfig \
  -n ai-native-paas-system wait --for=condition=complete --timeout=15m \
  job/iteration-5-control-plane-postgres-tests-7977261
```

The checked-in Job uses the resolved registry digest. The Git revision is
recorded separately to bind source provenance to the image evidence.
