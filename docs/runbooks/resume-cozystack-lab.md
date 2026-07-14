# Resume the Moscow Cozystack lab after Timeweb balance top-up

Prerequisites:

- Timeweb account balance covers one more dedicated 8 vCPU / 32 GiB / 100 GiB
  server, three 260 GiB data disks and the floating IP;
- state-service loopback port-forward is active;
- `TWC_TOKEN`, `TF_HTTP_USERNAME`, `TF_HTTP_PASSWORD`,
  `TF_VAR_state_passphrase`, `TF_VAR_project_id` and the current operator
  `/32` in `TF_VAR_management_cidrs` are exported.
- `TF_VAR_enabled_nodes` is unset (the default restores `cp-1`, `cp-2`, and
  `cp-3`); the two-node override is only for securing the degraded bootstrap.

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

Do not apply unless the plan has zero destroys. The expected remaining graph is
one server, three data disks, firewall/floating IP resources, Talos machine
configuration, one etcd bootstrap and kubeconfig retrieval. After apply, require:

1. all three Talos APIs reachable;
2. all Kubernetes nodes Ready;
3. a second plan with zero drift;
4. Cozystack installation and PostgreSQL/Redis/S3 lifecycle plus backup/restore
   gates before declaring Phase 1 green.
