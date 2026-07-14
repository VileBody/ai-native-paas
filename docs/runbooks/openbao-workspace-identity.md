# OpenBao workspace identity bootstrap

Run this only after the Shamir initialization/unseal ceremony and Kubernetes
auth bootstrap. The `workspace-manager` policy can issue one constrained
client-certificate role and synchronously revoke exact lease IDs; it cannot
configure PKI, read arbitrary secrets, use issuer-overriding endpoints, or
revoke prefixes.

The commands below are operator actions from an authenticated OpenBao shell.
Replace the trust domain only if `WORKSPACE_TRUST_DOMAIN` is changed at the
same time.

```sh
bao secrets enable -path=workspace-pki pki
bao secrets tune -max-lease-ttl=87600h workspace-pki

bao write workspace-pki/root/generate/internal \
  common_name=workspace.platform.example.com \
  ttl=87600h \
  key_type=ec \
  key_bits=256

bao write workspace-pki/roles/workspace-agent \
  issuer_ref=default \
  ttl=15m \
  max_ttl=15m \
  require_cn=false \
  allowed_uri_sans='spiffe://workspace.platform.example.com/tenant/*/project/*/workspace/*/task/*/agent/*' \
  allow_ip_sans=false \
  allow_localhost=false \
  server_flag=false \
  client_flag=true \
  code_signing_flag=false \
  email_protection_flag=false \
  key_type=ec \
  key_bits=256 \
  no_store=true \
  generate_lease=false

bao policy write workspace-manager \
  deploy/admin/openbao/policies/workspace-manager.hcl

bao write auth/kubernetes/role/workspace-manager \
  bound_service_account_names=workspace-manager \
  bound_service_account_namespaces=ai-native-paas-system \
  policies=workspace-manager \
  audience=openbao \
  token_ttl=15m \
  token_max_ttl=30m
```

The OpenBao Agent Injector must render the Kubernetes-authenticated workload
token to `/run/openbao/token` with mode `0400`. The manager rereads that file
for every issuance/revocation, so token rotation does not require a restart.
Never place a root token or static PKI master credential in the Deployment.

Verification:

```sh
bao read workspace-pki/roles/workspace-agent
bao token capabilities workspace-pki/issue/workspace-agent
bao token capabilities sys/leases/revoke
```

Expected certificate properties are enforced again by the manager before a
private key enters cloud-init: exact SPIFFE URI, client-auth only, trusted CA,
and no more than 15 minutes of validity. Workspace destruction closes the
certificate-bound session immediately; expired bootstrap certificates cannot
be reused to bind another workspace/provider identity.
