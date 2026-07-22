# Commerce API

The live control-plane deployment uses PostgreSQL and exposes two distinct
interfaces: a ClusterIP human API protected by OIDC and a TLS 1.3 internal API
that requires the `workspace-manager` SPIFFE client certificate. The internal
identity receives only `commerce.workspace_budget:write`, which is sufficient
to reserve and commit workspace command seconds and cannot access other
Commerce routes.

`commerce-api-settings` is intentionally not stored in Git. During the
pre-beta live window its OIDC client ID may be a fail-closed audience that has
no issued tokens; before release it must be replaced by the registered GitLab
OIDC application client ID. `commerce-api-mtls` is issued by OpenBao through
`scripts/sync-openbao-admin-service-mtls.sh`.
