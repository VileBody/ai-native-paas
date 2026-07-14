# Resume the Moscow Cozystack lab for a scheduled live gate

The provider state is intentionally empty after the 2026-07-15 cost-standby
teardown. The previous partial bootstrap is preserved in the encrypted recovery
bundle documented in
`docs/evidence/phase-1/cozystack-lab-cost-standby-2026-07-15.md`; do not restore
that historical state over the empty live state merely to recreate the lab.

Prerequisites:

- a scheduled gate and budget cover three dedicated 8 vCPU / 32 GiB / 100 GiB
  servers, three 260 GiB data disks and the floating IP for the gate duration;
- Timeweb confirms that three instances of preset `6633` can be provisioned in
  `msk-1`; previous creates reached terminal provider state `error`, so balance
  alone is not sufficient evidence;
- state-service loopback port-forward is active;
- `TWC_TOKEN`, `TF_HTTP_USERNAME`, `TF_HTTP_PASSWORD`,
  `TF_VAR_state_passphrase`, `TF_VAR_project_id` and the current operator
  `/32` in `TF_VAR_management_cidrs` are exported.
- `TF_VAR_lab_enabled=true` is explicitly exported for the scheduled live gate;
  its default is `false` so an ordinary development plan cannot recreate the
  dedicated-CPU nodes.
- `TF_VAR_cost_guard_acknowledgement=CREATE-3X-DEDICATED-CPU-COZYSTACK-LAB`
  is explicitly exported after the budget window is approved; the plan fails
  closed without this second signal.
- `TF_VAR_enabled_nodes` is unset so the default creates `cp-1`, `cp-2` and
  `cp-3` together.

Run from the repository root:

```sh
root="$PWD"
tofu -chdir=infra/stacks/cozystack-lab plan \
  -out="$root/.state-backend/cozystack-lab-resume.tfplan"
tofu -chdir=infra/stacks/cozystack-lab show -no-color \
  "$root/.state-backend/cozystack-lab-resume.tfplan"
tofu -chdir=infra/stacks/cozystack-lab apply \
  "$root/.state-backend/cozystack-lab-resume.tfplan"
```

Do not apply unless the plan has zero destroys and contains the complete
three-node graph: one VPC, three servers, three data disks, firewall/floating IP
resources, Talos machine configuration, one etcd bootstrap and kubeconfig
retrieval. After apply, require:

1. all three Talos APIs reachable;
2. all Kubernetes nodes Ready;
3. a second plan with zero drift;
4. Cozystack installation and PostgreSQL/Redis/S3 lifecycle plus backup/restore
   gates before declaring Phase 1 green.

After the scheduled gate, retain the encrypted state/evidence bundle and run a
full saved-plan destroy. Return `TF_VAR_lab_enabled=false` and require a final
zero-change plan.
