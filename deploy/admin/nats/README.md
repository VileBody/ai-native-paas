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
```
