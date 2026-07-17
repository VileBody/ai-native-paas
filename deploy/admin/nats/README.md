# NATS JetStream admin-cluster release

- Chart: `nats/nats` `2.14.2`
- Chart SHA-256: `86e0fbfe000b6d7fe70a3032ad3a667252f2d3dd2022e95f7330704d947d0f84`
- NATS image digest: `sha256:952d157e28d5394a211229bd57a7b37ff9f184e58e2c8486a08fa909fd254e32`

The release uses three JetStream replicas, TLS for client and route traffic,
token authentication, one retained 20 GiB Timeweb NVMe PVC per replica and
default-deny network policies. Authentication and CA material are generated
under the ignored `.operator/nats` custody directory and reconciled into
Kubernetes Secrets without printing values.

```bash
./scripts/deploy-nats.sh
NATS_STREAM_REPLICAS=3 ./scripts/configure-nats-streams.sh
```

The stream reconciler creates four fail-closed, file-backed streams with a
seven-day maximum age and server-side message deduplication:

- `PLATFORM_EVENTS` for source/build/runtime/attachments/commerce/agent events;
- `PLATFORM_OPERATIONS` for kernel and operation state;
- `WORKSPACE_COMMANDS` for disposable workspace command delivery;
- `PLATFORM_USAGE` for usage ledger ingestion.

Streams deny ad-hoc delete and purge. Run with `NATS_STREAM_REPLICAS=1` only
after the documented JetStream backup and admin dev-mode transition; restore
three replicas before a resilience gate or beta window.

`kernel-api` uses a synchronous JetStream publisher for its transactional
outbox. Production startup requires `NATS_URL` and the existing token via
`NATS_AUTH_TOKEN`; `NATS_CA_FILE` must point at the mounted server CA. Optional
client-certificate or NATS credentials-file authentication can be supplied with
`NATS_CLIENT_CERT_FILE`/`NATS_CLIENT_KEY_FILE` or `NATS_CREDS_FILE`. Each event
is published to `kernel.events`, requires an acknowledgement from
`PLATFORM_OPERATIONS`, and uses its event id as `Nats-Msg-Id`.
