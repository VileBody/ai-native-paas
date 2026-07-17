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

The stack now has `cozystack_profile=off`, `lab_enabled=false`, and
`ipv4_enabled=false` by default. With those defaults, a live remote-state plan
reports no provider resource actions. Future `smoke` and `provider_gate`
profiles have fixed three-node shapes, separate exact cost acknowledgements,
and require the independently authorized IPv4 gate. A non-zero profile can only
be entered from `off`; moving between non-zero sizes requires backup and full
recreation so disks are never shrunk in place.

Verification proved the configuration validates, an enabled profile without
IPv4/cost/transition guards is rejected, and the default off profile produces
zero live provider resource actions.
