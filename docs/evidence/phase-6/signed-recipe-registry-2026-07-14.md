# Signed recipe registry and policy — 2026-07-14

Status: `DB_GREEN`.

Implemented:

- Canonical Ed25519 signing and verification binds recipe identity, semantic
  version, driver, schema, lifecycle capabilities, immutable artifact digests,
  declared Kubernetes permissions and operational procedures.
- Active recipe versions are stored in PostgreSQL with payload-consistency and
  append-only UPDATE/DELETE triggers; exact replay is idempotent and changed
  content requires a new version.
- Resolution selects the highest policy-allowed semantic version and emits an
  exact lock containing content/signature and chart/image/module digests.
- Stateful recipes cannot be signed without health, backup, restore, upgrade,
  removal and retention procedures.
- Rendered resources must match the declared API group, kind and scope.
- Signed recipes may allow an exact namespaced product-operator CR. The same CR
  through the untrusted custom path is rejected.
- Custom Helm/OpenTofu-style installs remain possible for a bounded set of
  namespaced standard resources, but are marked untrusted and require approval.

Canonical executable evidence:

- `TestRecipe_ActivatedVersionIsImmutableAndSigned`
- `TestRecipe_ResolutionPinsExactVersionAndArtifactDigests`
- `TestRecipe_DeclaredPermissionsMatchRenderedResources`
- `TestRecipe_HealthBackupUpgradeAndRemovalProceduresAreComplete`
- `TestRecipe_CustomAdHocInstallIsAllowedWithinStricterPolicy`
- `TestRuntime_ProductOperatorCRAllowedOnlyByRecipePolicy`

Live PostgreSQL environment:

- `ai-native-paas-user-test/user-postgres` in the Timeweb test cluster through
  a temporary local port-forward, closed after the test.
- The admin/control-plane PostgreSQL database was not used.

Matrix effect:

- A5.8, A5.9, A5.12, A5.13, A5.14 and R12 are now `REUSED` executable
  evidence.
- The matrix discovers 942 Go test/fuzz targets: `REUSED 76`, `NEW 15`,
  `LIVE_ONLY 66`, with zero unmapped requirements.

Scope boundary:

- This is the registry/policy core. It does not claim the live Cozystack
  lifecycle of each seeded recipe or the final external recipe-catalog API.
