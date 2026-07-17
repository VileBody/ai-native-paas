# Workspace manager

The manager is a PostgreSQL-backed, two-replica production deployment. It owns
no shell and sends commands only to disposable Timeweb VMs over an outbound
workspace-agent session. Provider credentials and encrypted-log keys are files
from `workspace-manager-secrets`; OpenBao authentication uses the Kubernetes
service account through the injector and its renewable token file.

The manifest is intentionally not self-bootstrapping. Apply it only after:

1. the OpenBao Shamir ceremony, Kubernetes auth role `workspace-manager`,
   workspace PKI and policy are configured;
2. public CA material is copied with `./scripts/sync-openbao-client-ca.sh` to
   `openbao-client-ca`, and
   `workspace-manager-tls` contains the final server certificate/key plus the
   workspace client CA;
3. the current signed workspace VM artifact is imported and locked in the
   `workspace-images` remote state;
4. the workspace NAT router exists and the preserved legacy edge routes a
   dedicated TLS listener to the internal `workspace-manager` ClusterIP;
5. `workspace-manager-release` contains the exact project/configurator/VPC/image
   values and the final agent/gateway URLs and `/32` gateway policy.

`TIMEWEB_WORKSPACE_CONFIGURATOR_ID=31`, bandwidth `1000` and system disk
`40960` match the current Moscow standard configurator constraints. Re-read the
provider before changing them.

The deployment must not be applied while any release input is a placeholder.
The manifest never creates a LoadBalancer or IPv4 of its own.

Before applying the workload, run
`./scripts/verify-openbao-workspace-manager-auth.sh`. It performs a disposable
Kubernetes-auth login using the `workspace-manager` ServiceAccount, proves the
expected PKI and credential-envelope capabilities, proves Transit signing is
denied, revokes its token, and deletes the test Job.

Then run `./scripts/verify-openbao-workspace-manager-injector.sh`. This uses
the same injector annotations and named projected-token volume as the
deployment and verifies only the injected token file's presence and mode.

The role requires `audience=openbao`. The manifest therefore replaces the
default ServiceAccount projection with a 15-minute `openbao`-audience projected
token at the standard path and names that volume for the OpenBao Agent Injector.
Do not restore `automountServiceAccountToken: true` or add a second token mount.

Workspace logs use a dedicated Timeweb S3 user restricted to the workspace-log
bucket. Before applying the workload, synchronize it from private `0600` files
outside the repository:

```bash
export WORKSPACE_LOG_S3_ACCESS_KEY_FILE="$HOME/.config/ai-native-paas/timeweb-s3/workspace-logs.access-key"
export WORKSPACE_LOG_S3_SECRET_KEY_FILE="$HOME/.config/ai-native-paas/timeweb-s3/workspace-logs.secret-key"
./scripts/sync-workspace-manager-secrets.sh
```

The script deliberately has no fallback to `tofu output`; the Timeweb main S3
credential must never be installed in a workload.
