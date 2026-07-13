CREATE SCHEMA IF NOT EXISTS runtime;

CREATE TABLE IF NOT EXISTS runtime.applications (
    id text PRIMARY KEY,
    tenant_id text NOT NULL,
    project_id text NOT NULL,
    name text NOT NULL,
    lifecycle text NOT NULL,
    version bigint NOT NULL CHECK (version > 0),
    payload jsonb NOT NULL,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS runtime_applications_tenant_name_uq ON runtime.applications (tenant_id, lower(name));

CREATE TABLE IF NOT EXISTS runtime.environments (
    id text PRIMARY KEY,
    tenant_id text NOT NULL,
    application_id text NOT NULL,
    name text NOT NULL,
    namespace text NOT NULL UNIQUE,
    is_default boolean NOT NULL,
    active_release_id text NOT NULL DEFAULT '',
    placement_id text NOT NULL DEFAULT '',
    version bigint NOT NULL CHECK (version > 0),
    payload jsonb NOT NULL,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS runtime_environments_application_name_uq ON runtime.environments (application_id, lower(name));
CREATE UNIQUE INDEX IF NOT EXISTS runtime_environments_one_default_uq ON runtime.environments (application_id) WHERE is_default;
CREATE INDEX IF NOT EXISTS runtime_environments_tenant_idx ON runtime.environments (tenant_id, application_id);

CREATE TABLE IF NOT EXISTS runtime.runtime_cells (
    id text PRIMARY KEY,
    region text NOT NULL,
    state text NOT NULL CHECK (state IN ('ACTIVE','DRAINING','OFFLINE')),
    capacity_units integer NOT NULL CHECK (capacity_units > 0),
    allocated_units integer NOT NULL CHECK (allocated_units >= 0 AND allocated_units <= capacity_units),
    gitops_repository text NOT NULL,
    version bigint NOT NULL CHECK (version > 0),
    payload jsonb NOT NULL,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL
);
CREATE INDEX IF NOT EXISTS runtime_cells_placement_idx ON runtime.runtime_cells (region, state, allocated_units);

CREATE TABLE IF NOT EXISTS runtime.placements (
    id text PRIMARY KEY,
    tenant_id text NOT NULL,
    environment_id text NOT NULL,
    cell_id text NOT NULL,
    current boolean NOT NULL,
    units integer NOT NULL CHECK (units > 0),
    version bigint NOT NULL CHECK (version > 0),
    payload jsonb NOT NULL,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS runtime_placements_one_current_uq ON runtime.placements (environment_id) WHERE current;
CREATE INDEX IF NOT EXISTS runtime_placements_cell_idx ON runtime.placements (cell_id, current);

CREATE TABLE IF NOT EXISTS runtime.placement_migrations (
    id text PRIMARY KEY,
    tenant_id text NOT NULL,
    environment_id text NOT NULL,
    from_placement_id text NOT NULL,
    target_cell_id text NOT NULL,
    state text NOT NULL,
    payload jsonb NOT NULL,
    created_at timestamptz NOT NULL
);

CREATE TABLE IF NOT EXISTS runtime.releases (
    id text PRIMARY KEY,
    tenant_id text NOT NULL,
    application_id text NOT NULL,
    environment_id text NOT NULL,
    identity text NOT NULL,
    rollback_of text NOT NULL DEFAULT '',
    artifact_id text NOT NULL,
    repository text NOT NULL,
    digest text NOT NULL CHECK (digest ~ '^sha256:[0-9a-f]{64}$'),
    media_type text NOT NULL,
    state text NOT NULL CHECK (state IN ('CREATED','VALIDATED','COMMITTED_TO_GITOPS','DEPLOYING','ACTIVE','FAILED','SUPERSEDED')),
    version bigint NOT NULL CHECK (version > 0),
    payload jsonb NOT NULL,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS runtime_releases_identity_uq ON runtime.releases (tenant_id, environment_id, identity) WHERE rollback_of = '';
CREATE UNIQUE INDEX IF NOT EXISTS runtime_releases_one_active_uq ON runtime.releases (environment_id) WHERE state = 'ACTIVE';
CREATE INDEX IF NOT EXISTS runtime_releases_environment_idx ON runtime.releases (environment_id, created_at);

CREATE TABLE IF NOT EXISTS runtime.deployments (
    id text PRIMARY KEY,
    tenant_id text NOT NULL,
    application_id text NOT NULL,
    environment_id text NOT NULL,
    release_id text NOT NULL UNIQUE,
    placement_id text NOT NULL,
    phase text NOT NULL CHECK (phase IN ('PENDING','GIT_COMMITTED','ARGO_SYNCING','ROLLING_OUT','READY','DEGRADED','FAILED')),
    git_commit_sha text NOT NULL DEFAULT '',
    version bigint NOT NULL CHECK (version > 0),
    payload jsonb NOT NULL,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL
);
CREATE INDEX IF NOT EXISTS runtime_deployments_environment_idx ON runtime.deployments (environment_id, created_at);

CREATE TABLE IF NOT EXISTS runtime.gitops_commits (
    id text PRIMARY KEY,
    tenant_id text NOT NULL,
    cell_id text NOT NULL,
    release_id text NOT NULL UNIQUE,
    deployment_id text NOT NULL UNIQUE,
    path text NOT NULL,
    manifest_hash text NOT NULL CHECK (manifest_hash ~ '^sha256:[0-9a-f]{64}$'),
    commit_sha text NOT NULL,
    payload jsonb NOT NULL,
    created_at timestamptz NOT NULL
);

CREATE TABLE IF NOT EXISTS runtime.quarantine (
    id text PRIMARY KEY,
    cell_id text NOT NULL,
    namespace text NOT NULL,
    kind text NOT NULL,
    name text NOT NULL,
    reason text NOT NULL,
    payload jsonb NOT NULL,
    observed_at timestamptz NOT NULL
);
CREATE INDEX IF NOT EXISTS runtime_quarantine_observed_idx ON runtime.quarantine (observed_at);

CREATE TABLE IF NOT EXISTS runtime.idempotency (
    tenant_id text NOT NULL,
    scope text NOT NULL,
    key text NOT NULL,
    request_hash text NOT NULL,
    resource_id text NOT NULL,
    created_at timestamptz NOT NULL,
    PRIMARY KEY (tenant_id, scope, key)
);

CREATE TABLE IF NOT EXISTS runtime.outbox (
    id text PRIMARY KEY,
    tenant_id text NOT NULL,
    topic text NOT NULL,
    aggregate_id text NOT NULL,
    payload jsonb NOT NULL,
    created_at timestamptz NOT NULL,
    published_at timestamptz
);
CREATE INDEX IF NOT EXISTS runtime_outbox_pending_idx ON runtime.outbox (created_at) WHERE published_at IS NULL;

CREATE TABLE IF NOT EXISTS runtime.audit (
    id text PRIMARY KEY,
    tenant_id text NOT NULL,
    actor_id text NOT NULL,
    action text NOT NULL,
    resource_type text NOT NULL,
    resource_id text NOT NULL,
    data jsonb NOT NULL,
    created_at timestamptz NOT NULL
);
CREATE INDEX IF NOT EXISTS runtime_audit_tenant_created_idx ON runtime.audit (tenant_id, created_at);
