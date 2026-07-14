# Managed PostgreSQL workspace outbox evidence — 2026-07-14

Evidence tier: `DB_GREEN`

## Bound revision

- Git revision: `0975f9862d0f633dad18fad1f76023895a9edb19`
- CI run: `application-attachments` run `29302703711`
- Test image tag: `ai-native-paas-registry.registry.twcstorage.ru/ai-native-paas-tests:0975f9862d0f633dad18fad1f76023895a9edb19`
- Resolved immutable image: `sha256:0e4505a8cac883ca2ac4cf8afc53b65e626eb0898c449b44b6d85ded84998e60`
- Kubernetes Job: `ai-native-paas-system/iteration-5-control-plane-postgres-tests-0975f98`

## Scope

This revision adds migration `004_outbox_delivery_leases.sql`, a durable
lease-based dispatcher for workspace reconciliation/command intents, and the
production `project-api` wiring that persists MCP workspace requests to the
managed database without acquiring provider authority.

The outbox claim uses `FOR UPDATE SKIP LOCKED`, a durable owner/expiry lease,
monotonic delivery attempts and an immutable event identity/payload. Command
scope is reconstructed from the stored command rather than the outbox payload.
An external effect may be replayed after a crash, but the provider/session
adapters receive the same aggregate identity and remain idempotent.

## Result

GitHub CI passed both the complete PostgreSQL suite and static/unit verification.
The exact diagnostic image then ran against Timeweb managed PostgreSQL through
the private admin VPC and reached `Complete=True` with one successful Pod and
zero retries.

`TestPostgres_WorkspaceOutboxLeaseHasOneWinnerAndCrashTakeover` proved:

- one winner under twelve concurrent claims;
- no takeover before lease expiry;
- takeover after expiry with a monotonic attempt counter;
- stale owners cannot acknowledge;
- acknowledgement is single-use;
- payload mutation remains rejected while an event is leased.

The same Job also passed durable agent ACK restart recovery and the complete
source/runtime/attachments/commerce/agent PostgreSQL regression set.

## Reproduction

```sh
kubectl --kubeconfig infra/timeweb/ai-native-paas-test.kubeconfig \
  apply -f infra/timeweb/control-plane-postgres-test-job.yaml

kubectl --kubeconfig infra/timeweb/ai-native-paas-test.kubeconfig \
  -n ai-native-paas-system wait --for=condition=complete --timeout=15m \
  job/iteration-5-control-plane-postgres-tests-0975f98
```

The checked-in Job is pinned to the registry digest. Database credentials are
loaded only from the existing Kubernetes Secret and are absent from this file,
the manifest and the test logs.
