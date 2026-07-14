# Workspace manager

The manager is a PostgreSQL-backed, two-replica production deployment. It owns
no shell and sends commands only to disposable Timeweb VMs over an outbound
workspace-agent session. Provider credentials and encrypted-log keys are files
from `workspace-manager-secrets`; OpenBao authentication uses the Kubernetes
service account through the injector and its renewable token file.

The manifest is intentionally not self-bootstrapping. Apply it only after:

1. the OpenBao Shamir ceremony, Kubernetes auth role `workspace-manager`,
   workspace PKI and policy are configured;
2. public CA material is copied to `openbao-client-ca`, and
   `workspace-manager-tls` contains the final server certificate/key plus the
   workspace client CA;
3. the current signed workspace VM artifact is imported and locked in the
   `workspace-images` remote state;
4. Timeweb balance is restored, the workspace NAT router and both LoadBalancer
   services exist, and DNS/TLS names resolve;
5. `workspace-manager-release` contains the exact project/configurator/VPC/image
   values and the final agent/gateway URLs and `/32` gateway policy.

`TIMEWEB_WORKSPACE_CONFIGURATOR_ID=31`, bandwidth `1000` and system disk
`40960` match the current Moscow standard configurator constraints. Re-read the
provider before changing them.

The deployment must not be applied while any release input is a placeholder.
