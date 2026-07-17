CREATE SCHEMA IF NOT EXISTS build;

CREATE TABLE IF NOT EXISTS build.schema_migrations (
    version text PRIMARY KEY,
    checksum text NOT NULL,
    applied_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS build.builds (
    id text PRIMARY KEY,
    tenant_id text NOT NULL,
    identity text NOT NULL,
    attempt integer NOT NULL CHECK (attempt > 0),
    source_revision jsonb NOT NULL,
    config jsonb NOT NULL,
    builder_digest text NOT NULL CHECK (builder_digest ~ '^sha256:[0-9a-f]{64}$'),
    run_image_digest text NOT NULL CHECK (run_image_digest ~ '^sha256:[0-9a-f]{64}$'),
    platform_build_version text NOT NULL,
    state text NOT NULL CHECK (state IN (
        'QUEUED','FETCHING_SOURCE','DETECTING','BUILDING','EXPORTING','SCANNING','SIGNING',
        'SUCCEEDED','FAILED_USER_CODE','FAILED_PLATFORM','CANCELED','SUPERSEDED','TIMED_OUT'
    )),
    correlation_id text NOT NULL,
    original_correlation_id text NOT NULL,
    artifact_id text,
    failure_code text NOT NULL DEFAULT '',
    failure_message text NOT NULL DEFAULT '',
    retryable boolean NOT NULL DEFAULT false,
    version bigint NOT NULL CHECK (version > 0),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    started_at timestamptz,
    completed_at timestamptz,
    UNIQUE (tenant_id, identity, attempt)
);

CREATE UNIQUE INDEX IF NOT EXISTS build_one_active_or_successful_identity
    ON build.builds (tenant_id, identity)
    WHERE state NOT IN ('FAILED_USER_CODE','FAILED_PLATFORM','CANCELED','SUPERSEDED','TIMED_OUT');
CREATE INDEX IF NOT EXISTS build_project_history_idx
    ON build.builds (tenant_id, (source_revision->>'project_id'), created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS build_state_queue_idx
    ON build.builds (state, created_at, id)
    WHERE state = 'QUEUED';

CREATE TABLE IF NOT EXISTS build.artifacts (
    id text PRIMARY KEY,
    tenant_id text NOT NULL,
    build_id text NOT NULL UNIQUE REFERENCES build.builds(id) ON DELETE RESTRICT,
    repository text NOT NULL,
    digest text NOT NULL CHECK (digest ~ '^sha256:[0-9a-f]{64}$'),
    media_type text NOT NULL,
    state text NOT NULL CHECK (state IN ('DISCOVERED','QUARANTINED','SCANNED','SIGNED','RELEASABLE','REJECTED')),
    sbom_digest text NOT NULL DEFAULT '' CHECK (sbom_digest = '' OR sbom_digest ~ '^sha256:[0-9a-f]{64}$'),
    sbom_media_type text NOT NULL DEFAULT '',
    provenance_digest text NOT NULL DEFAULT '' CHECK (provenance_digest = '' OR provenance_digest ~ '^sha256:[0-9a-f]{64}$'),
    provenance_media_type text NOT NULL DEFAULT '',
    rejection_code text NOT NULL DEFAULT '',
    rejection_notes jsonb NOT NULL DEFAULT '[]'::jsonb,
    version bigint NOT NULL CHECK (version > 0),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL
);
CREATE INDEX IF NOT EXISTS build_artifact_digest_idx ON build.artifacts (tenant_id, repository, digest);
CREATE INDEX IF NOT EXISTS build_artifact_releasable_idx ON build.artifacts (tenant_id, state, created_at DESC) WHERE state = 'RELEASABLE';

CREATE TABLE IF NOT EXISTS build.scan_results (
    artifact_id text PRIMARY KEY REFERENCES build.artifacts(id) ON DELETE RESTRICT,
    scanner text NOT NULL,
    policy_version text NOT NULL,
    passed boolean NOT NULL,
    highest_severity text NOT NULL DEFAULT '',
    findings_digest text NOT NULL CHECK (findings_digest ~ '^sha256:[0-9a-f]{64}$'),
    reasons jsonb NOT NULL DEFAULT '[]'::jsonb,
    scanned_at timestamptz NOT NULL
);

CREATE TABLE IF NOT EXISTS build.signature_records (
    artifact_id text PRIMARY KEY REFERENCES build.artifacts(id) ON DELETE RESTRICT,
    issuer text NOT NULL,
    algorithm text NOT NULL,
    digest text NOT NULL CHECK (digest ~ '^sha256:[0-9a-f]{64}$'),
    signature text NOT NULL,
    signed_at timestamptz NOT NULL
);

CREATE TABLE IF NOT EXISTS build.log_refs (
    build_id text PRIMARY KEY REFERENCES build.builds(id) ON DELETE RESTRICT,
    backend text NOT NULL,
    locator text NOT NULL,
    size_bytes bigint NOT NULL DEFAULT 0 CHECK (size_bytes >= 0),
    digest text NOT NULL DEFAULT '' CHECK (digest = '' OR digest ~ '^sha256:[0-9a-f]{64}$'),
    created_at timestamptz NOT NULL
);

CREATE TABLE IF NOT EXISTS build.idempotency (
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

CREATE TABLE IF NOT EXISTS build.outbox (
    id text PRIMARY KEY,
    topic text NOT NULL,
    aggregate_id text NOT NULL,
    payload jsonb NOT NULL,
    created_at timestamptz NOT NULL,
    published_at timestamptz
);
CREATE INDEX IF NOT EXISTS build_outbox_unpublished_idx
    ON build.outbox (created_at, id) WHERE published_at IS NULL;

CREATE TABLE IF NOT EXISTS build.audit (
    id text PRIMARY KEY,
    tenant_id text NOT NULL,
    actor_id text NOT NULL,
    action text NOT NULL,
    resource_type text NOT NULL,
    resource_id text NOT NULL,
    data jsonb NOT NULL,
    created_at timestamptz NOT NULL
);
