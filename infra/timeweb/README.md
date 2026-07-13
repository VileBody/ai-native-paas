# Timeweb Cloud test environment

This Terraform stack creates a dedicated cluster and does not reuse the
`call-analytics-k8s` cluster or any of its Kubernetes resources. The current
Timeweb API token cannot create projects, so the owning project is supplied as
an input.

It provisions:

- one non-HA Kubernetes control plane in Moscow;
- one 2 CPU / 4 GiB worker node;
- one private VPC shared by Kubernetes and the control-plane database;
- one managed PostgreSQL 17 cluster for platform metadata.

The managed database is private-only. Run migrations and application traffic
from Kubernetes; do not expose PostgreSQL to the public internet.

User-requested databases are intentionally not part of this stack. The provider
layer creates those workloads inside Kubernetes and accounts for their usage.

The API token is read from `TWC_TOKEN`; never put it in a tfvars file:

```shell
export TWC_TOKEN="..."
terraform -chdir=infra/timeweb init
terraform -chdir=infra/timeweb plan -var='project_id=1234567'
terraform -chdir=infra/timeweb apply -var='project_id=1234567'
```

Terraform state contains generated database credentials and must stay local or
be moved to an encrypted remote backend before other operators use this stack.
