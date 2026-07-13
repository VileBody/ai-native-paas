CREATE SCHEMA IF NOT EXISTS source;
CREATE TABLE IF NOT EXISTS source.schema_migrations (
    version text PRIMARY KEY,
    checksum text NOT NULL,
    applied_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS source.projects (
    id text PRIMARY KEY,
    tenant_id text NOT NULL,
    name text NOT NULL,
    slug text NOT NULL,
    version bigint NOT NULL CHECK (version > 0),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    UNIQUE (tenant_id, slug)
);
CREATE TABLE IF NOT EXISTS source.repositories (
    id text PRIMARY KEY,
    tenant_id text NOT NULL,
    project_id text NOT NULL UNIQUE REFERENCES source.projects(id) ON DELETE RESTRICT,
    provider text NOT NULL,
    provider_namespace_id bigint NOT NULL CHECK (provider_namespace_id > 0),
    provider_project_id bigint,
    provider_path text NOT NULL DEFAULT '',
    web_url text NOT NULL DEFAULT '',
    default_branch text NOT NULL DEFAULT 'main',
    state text NOT NULL,
    last_error text NOT NULL DEFAULT '',
    correlation_id text NOT NULL UNIQUE,
    version bigint NOT NULL CHECK (version > 0),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    UNIQUE NULLS NOT DISTINCT (provider, provider_project_id)
);
CREATE TABLE IF NOT EXISTS source.branch_heads (
    repository_id text NOT NULL REFERENCES source.repositories(id) ON DELETE CASCADE,
    name text NOT NULL,
    commit_sha text NOT NULL DEFAULT '',
    last_event_id text NOT NULL DEFAULT '',
    last_event_at timestamptz,
    observed_at timestamptz,
    version bigint NOT NULL CHECK (version > 0),
    PRIMARY KEY (repository_id, name)
);
CREATE TABLE IF NOT EXISTS source.merge_requests (
    repository_id text NOT NULL REFERENCES source.repositories(id) ON DELETE CASCADE,
    provider_iid bigint NOT NULL,
    source_branch text NOT NULL,
    target_branch text NOT NULL,
    head_sha text NOT NULL DEFAULT '',
    state text NOT NULL,
    version bigint NOT NULL CHECK (version > 0),
    updated_at timestamptz NOT NULL,
    PRIMARY KEY (repository_id, provider_iid)
);
CREATE TABLE IF NOT EXISTS source.workspaces (
    id text PRIMARY KEY,
    tenant_id text NOT NULL,
    repository_id text NOT NULL REFERENCES source.repositories(id) ON DELETE RESTRICT,
    branch text NOT NULL,
    base_sha text NOT NULL,
    commit_sha text NOT NULL DEFAULT '',
    directory text NOT NULL DEFAULT '',
    credential_id text NOT NULL DEFAULT '',
    state text NOT NULL,
    last_error text NOT NULL DEFAULT '',
    expires_at timestamptz NOT NULL,
    version bigint NOT NULL CHECK (version > 0),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL
);
CREATE TABLE IF NOT EXISTS source.idempotency (
    tenant_id text NOT NULL,
    key text NOT NULL,
    command text NOT NULL,
    request_hash text NOT NULL,
    result jsonb NOT NULL DEFAULT '{}'::jsonb,
    completed boolean NOT NULL DEFAULT false,
    created_at timestamptz NOT NULL,
    completed_at timestamptz,
    PRIMARY KEY (tenant_id, key)
);
CREATE TABLE IF NOT EXISTS source.webhook_receipts (
    provider text NOT NULL,
    event_id text NOT NULL,
    body_hash text NOT NULL,
    received_at timestamptz NOT NULL,
    PRIMARY KEY (provider, event_id)
);
CREATE TABLE IF NOT EXISTS source.outbox (
    id text PRIMARY KEY,
    topic text NOT NULL,
    aggregate_id text NOT NULL,
    payload jsonb NOT NULL,
    created_at timestamptz NOT NULL,
    published_at timestamptz
);
CREATE TABLE IF NOT EXISTS source.audit (
    id text PRIMARY KEY,
    tenant_id text NOT NULL,
    actor_id text NOT NULL,
    action text NOT NULL,
    resource_type text NOT NULL,
    resource_id text NOT NULL,
    data jsonb NOT NULL,
    created_at timestamptz NOT NULL
);
CREATE INDEX IF NOT EXISTS source_repositories_reconcile_idx ON source.repositories (state, updated_at, id);
CREATE INDEX IF NOT EXISTS source_outbox_unpublished_idx ON source.outbox (created_at, id) WHERE published_at IS NULL;
CREATE INDEX IF NOT EXISTS source_workspaces_expiry_idx ON source.workspaces (expires_at) WHERE state NOT IN ('COMPLETED','DELETED');
