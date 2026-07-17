# Admin capacity modes

The admin cluster has three explicit modes. `ha` is the release/resilience mode:
three system workers, three OpenBao Raft members, two injectors, and three NATS
JetStream members. `dev` is the cost-saving mode: one system worker and one
replica of each stateful service. `off` is full system-pool standby: OpenBao,
its injector and NATS are scaled to zero and the system node group is removed.
The managed PostgreSQL, encrypted S3 backups and retained PVCs are not destroyed
by either transition. The state service remains on the CI worker so the HTTP
OpenTofu backend can restore the system pool.

Never enter `dev` before both HA gates have passed. First create and verify an
OpenBao Raft snapshot in the protected S3 destination and a NATS JetStream
backup/export. Record the evidence IDs. Then deploy each chart with its
service-specific acknowledgement:

```sh
ADMIN_CAPACITY_MODE=dev \
ADMIN_DEV_SNAPSHOT_ACK=OPENBAO-RAFT-SNAPSHOT-VERIFIED \
scripts/deploy-openbao.sh

ADMIN_CAPACITY_MODE=dev \
ADMIN_DEV_SNAPSHOT_ACK=NATS-JETSTREAM-BACKUP-VERIFIED \
scripts/deploy-nats.sh
```

After both StatefulSets are healthy at one replica, set
`admin_capacity_mode="dev"` in the reviewed admin OpenTofu inputs and apply the
one-worker plan. Do not reduce the workers first.

To enter full standby, deploy both charts with `ADMIN_CAPACITY_MODE=off` and the
same snapshot acknowledgements. Verify both StatefulSets have zero replicas,
the OpenBao injector Deployment and admission webhook are absent, the NATS
helper deployment is absent, every PVC is still Bound and `state-service` is
healthy on the CI worker. Reconcile the managed CSI controller onto the CI pool
before removing the system pool:

```sh
KUBECONFIG=infra/timeweb/ai-native-paas-test.kubeconfig \
ADMIN_CAPACITY_MODE=off \
scripts/reconcile-timeweb-csi.sh
```

The off reconcile also gives CoreDNS a single-node-safe replacement strategy.
Then set
`admin_capacity_mode="off"` and apply the reviewed plan that deletes only the
system node group. Never delete the retained OpenBao or NATS PVCs during an off
window.

Before a resilience gate or invited beta window, reverse the order: apply
`admin_capacity_mode="ha"` to restore three workers, wait until all are Ready,
reconcile CSI with `ADMIN_CAPACITY_MODE=ha`, then deploy both charts with
`ADMIN_CAPACITY_MODE=ha`. Require three healthy
members, OpenBao unsealed quorum, JetStream current replicas, and a zero-drift
admin plan. A failed scale-up aborts the beta window; it must never be papered
over by running the release on the single-node development profile.
