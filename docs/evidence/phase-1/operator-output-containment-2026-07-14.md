# Operator-output credential containment — 2026-07-14

Evidence tier: `SECURITY_GREEN` after containment

An invalid Kubernetes Secret patch printed the existing Secret object in local
operator tool output. No credential was committed to Git, but every printed
credential was treated as compromised.

Containment completed in the same operator session:

- all four scoped HTTP state credentials rotated and the old set revoked;
- managed PostgreSQL application password rotated through OpenTofu;
- `control-plane-postgres` and the state-service database DSN reconciled;
- Timeweb account-wide S3 credentials reset through the authenticated provider
  endpoint;
- old S3 credentials explicitly tested as rejected and new credentials tested
  as accepted;
- state-service reconciled and rolled after both database and S3 rotations;
- `state-bootstrap` migrated from local-only state into its own encrypted HTTP
  namespace;
- bootstrap and workspace S3-owning states refreshed with the new credentials;
- HTTP authorization rechecked: admin `200`, cross-scope request `403`,
  state-bootstrap `200`;
- final admin, workspace-images and state-bootstrap plans: zero drift;
- repository Timeweb-token sentinel: PASS.

The Timeweb API token itself was never printed. The affected operator output
must still be handled as sensitive operational evidence and must not be copied
into tickets or release artifacts.
