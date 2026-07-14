# Cozystack lab cost standby — 2026-07-15

Evidence tier: `PROVIDER_STANDBY` with verified encrypted recovery.

## Backup decision

Velero was not applicable: the partial lab had no Kubernetes API, etcd,
Cozystack installation, workload objects, persistent data disks or application
volumes. The recoverable state was the OpenTofu/provider bootstrap state.

Before deletion, the operator created a mode-`0700` recovery bundle at:

```text
/Users/ergin/Backups/ai-native-paas/cozystack-lab-pre-destroy-2026-07-15T0005MSK
```

It contains the client-encrypted pre-destroy state, encrypted destroy plan,
all 16 state addresses, sanitized Timeweb server inventory, the pinned IaC
source archive, post-destroy/standby states and a separately permissioned copy
of the offline recovery passphrase. The passphrase successfully decrypted the
pre-destroy state in a streaming verification: serial `18`, lineage
`f749d476-f365-2270-968d-3646c6b6e446`, eight resource blocks and 16 instances.

The bundle checksum manifest was recalculated and compared byte-for-byte after
the final standby state was added:

```text
SHA256(SHA256SUMS) = 3198f8adcbf7b040737e84ed73aa1b9f8f8348e2c928b7f496fa2560391d4793
```

The encrypted backend remains versioned in S3 as the provider-side recovery
copy. The local recovery key still needs an independent off-machine/password-
manager copy for a complete disaster-recovery posture.

## Destruction result

The saved OpenTofu destroy plan was machine-checked before apply:

```text
0 add / 0 change / 16 destroy
```

It contained only the partial runtime-cell resources: two Talos bootstrap
servers, their firewalls and rules, Talos generated secrets and the dedicated
`192.168.74.0/24` VPC. The admin Kubernetes cluster, managed PostgreSQL, S3,
registry and admin PVCs were absent from the plan.

The exact saved plan completed successfully:

```text
Apply complete! Resources: 0 added, 0 changed, 16 destroyed.
```

Independent post-checks found zero `cozystack-*` servers through the Timeweb
API and zero resource addresses in the remote state. The final standby state is
serial `21`, keeps the original lineage and has zero resources.

The unrelated admin plane remained healthy after teardown: the managed cluster
reported `started`, all four admin workers were `Ready`, state-service was
`1/1`, sealed OpenBao remained `3/3` and NATS remained `3/3`.

## Cost guard

Preset `6633` was priced by the live provider API at `19,900 RUB/month` per
server on the deletion date. Removing both retained servers stops approximately
`39,800 RUB/month` of idle compute allocation.

The stack now has `lab_enabled=false` by default. With that default, a live
remote-state plan reports no changes and no provider resource actions. A future
live gate must deliberately export `TF_VAR_lab_enabled=true` and the separate
exact cost acknowledgement. The plan also rejects a partial node set. After its
evidence and backups are retained, the complete lab is destroyed again.

Verification proved all three guard states: the configuration validates, an
enabled lab without the exact acknowledgement is rejected during plan, and the
default standby profile produces a zero-change live remote-state plan.
