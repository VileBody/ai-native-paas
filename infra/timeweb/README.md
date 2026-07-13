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

The cloud test environment also uses a dedicated 5 GiB Container Registry,
`ai-native-paas-registry` (ID `24867`). It is attached to the
`ai-native-paas-user-test` and `kube-system` namespaces and provides the
`craas-ai-native-paas-registry` image pull secret. GitHub Actions publishes the
Iteration 5 test runner to this registry so cluster tests do not depend on
Docker Hub or GHCR egress.

The managed database is private-only. Run migrations and application traffic
from Kubernetes; do not expose PostgreSQL to the public internet.

User-requested databases are intentionally not part of this stack. The provider
layer creates those workloads inside Kubernetes and accounts for their usage.

The current test resource split is therefore:

- platform/control-plane state: private Timeweb managed PostgreSQL;
- user-requested services: namespace-scoped Kubernetes workloads;
- build and test images: the dedicated Timeweb Container Registry.

The API token is read from `TWC_TOKEN`; never put it in a tfvars file:

```shell
export TWC_TOKEN="..."
terraform -chdir=infra/timeweb init
terraform -chdir=infra/timeweb plan -var='project_id=1234567'
terraform -chdir=infra/timeweb apply -var='project_id=1234567'
```

Terraform state contains generated database credentials and must stay local or
be moved to an encrypted remote backend before other operators use this stack.

## Timeweb registry integration

Container Registry is not supported by the current Terraform provider. The
registry is created through the Timeweb API and attached to the namespace with:

```text
POST /api/v1/k8s/clusters/1099941/container-registry
{"registry_items":[{"registry_id":24867,"namespace":"ai-native-paas-user-test"},{"registry_id":24867,"namespace":"kube-system"}]}
```

Do not commit the registry token. Timeweb owns the generated Kubernetes pull
secret; CI credentials are stored as GitHub Actions secrets.

## Timeweb Cilium Envoy workaround

The initial `v1.35.6+k0s.0` installation could not pull Cilium Envoy from
`quay.io` because the worker repeatedly hit a TLS handshake timeout. The exact
upstream multi-architecture image was mirrored to the dedicated Timeweb
Container Registry without changing its digest. Reapply the namespaced pull
secret and DaemonSet override with:

```shell
export KUBECONFIG="infra/timeweb/ai-native-paas-test.kubeconfig"
./scripts/fix-timeweb-cilium-envoy.sh
```

Remove this override after Timeweb ships the Envoy image in its internal
registry or fixes worker access to `quay.io`.
