# Phase 2 evidence — NATS JetStream admin quorum

Date: 2026-07-14

## Deployed foundation

- Official NATS Helm chart `2.14.2`; downloaded chart SHA-256
  `86e0fbfe000b6d7fe70a3032ad3a667252f2d3dd2022e95f7330704d947d0f84`.
- NATS `2.14.2`, config reloader `0.23.0`, and nats-box `0.19.7` are
  referenced by immutable image digests.
- Three JetStream replicas are scheduled across three dedicated admin system
  workers. Each replica owns a retained 20 GiB RWO Timeweb NVMe PVC.
- Client and route traffic use certificates issued by a deployment-local CA.
  Client authentication uses a generated token stored only in a Kubernetes
  Secret. Anonymous access is disabled.
- Pod Security `restricted` is enforced. Server containers run as non-root,
  drop all Linux capabilities, disable privilege escalation, and use the
  runtime-default seccomp profile.
- Default-deny NetworkPolicy permits NATS client ingress only from the NATS and
  `ai-native-paas-system` namespaces; cluster routes remain namespace-local.

## Live verification

The live `ai-native-paas-test` admin cluster reported:

```text
Helm release: nats, revision 4, status deployed
StatefulSet: nats 3/3 Ready
nats-0: Running on worker-192.168.73.11
nats-1: Running on worker-192.168.73.9
nats-2: Running on worker-192.168.73.10
JetStream PVCs: 3 x 20Gi, Bound, nvme.network-drives.csi.timeweb.cloud
```

A connection using the same TLS context but with the authorization token
removed was rejected; the authenticated connection check then succeeded. A
temporary file-backed stream
`PLATFORM_BOOTSTRAP` with subject `platform.bootstrap.>` accepted a message and
reported one leader plus two current replicas on the three distinct servers.

## Recovery observations

The initial render exposed string-valued byte limits that NATS rejected; the
values are now integer bytes. The nats-box image also required a writable home
and explicit working directory. During the pre-data rollout, the two unusable
old pods were recreated after their stale configuration prevented quorum. The
final revision then converged with zero restarts on all server containers.

This proves the admin event-bus foundation and a live three-replica JetStream
write. It is not `OPS_GREEN`: durable platform streams, service publishers and
consumers, outbox/inbox crash recovery, alerting, backup, and failover drills
remain release gates.
