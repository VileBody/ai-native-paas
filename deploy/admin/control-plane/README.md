# Admin control plane APIs

`project-api.yaml` is the production PostgreSQL/GitLab/OIDC Project API and
Project MCP v2 deployment. It is deliberately exposed only as a ClusterIP until
the beta DNS name, certificate and authenticated ingress are installed.

The image is immutable and recorded in `images.lock.json`. The manifest contains
no secret values. Before applying it, an operator must create:

- `control-plane-postgres` with `DATABASE_URL` by running
  `scripts/sync-timeweb-control-plane-postgres-secret.sh`;
- `project-api-secrets` with `GITLAB_ADMIN_TOKEN` and a random, at least
  32-byte `ENROLLMENT_SIGNING_KEY`;
- `project-api-settings` with `GITLAB_NAMESPACE_ID`, `MCP_BASE_URL`,
  `OIDC_ISSUER` and `OIDC_CLIENT_ID`;
- `project-api-release` with the verified imported workspace VM image digest.

The GitLab token must be the bot for the dedicated private beta group, not a
personal or group-wide human token. `MCP_BASE_URL` must be the final HTTPS API
origin. OIDC memberships are seeded separately and remain invite-only.

Apply only after those inputs exist:

```bash
kubectl apply --server-side --field-manager=ai-native-paas \
  -f deploy/admin/control-plane/project-api.yaml
kubectl -n ai-native-paas-system rollout status deployment/project-api
```
