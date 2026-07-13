CREATE SCHEMA IF NOT EXISTS commerce;

CREATE TABLE IF NOT EXISTS commerce.plan_definitions (
    id text PRIMARY KEY,
    name text NOT NULL,
    version bigint NOT NULL CHECK (version > 0),
    payload jsonb NOT NULL,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL
);

CREATE TABLE IF NOT EXISTS commerce.plan_versions (
    id text PRIMARY KEY,
    definition_id text NOT NULL,
    policy_version text NOT NULL,
    number bigint NOT NULL CHECK (number > 0),
    state text NOT NULL CHECK (state IN ('DRAFT','ACTIVE','DISABLED')),
    effective_from timestamptz NOT NULL,
    currency text NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
    spec_hash text NOT NULL CHECK (spec_hash ~ '^[0-9a-f]{64}$'),
    version bigint NOT NULL CHECK (version > 0),
    payload jsonb NOT NULL,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    UNIQUE (definition_id, number)
);
CREATE INDEX IF NOT EXISTS commerce_plan_versions_effective_idx ON commerce.plan_versions (definition_id, effective_from, number);

CREATE TABLE IF NOT EXISTS commerce.subscriptions (
    id text PRIMARY KEY,
    tenant_id text NOT NULL,
    plan_version_id text NOT NULL,
    state text NOT NULL CHECK (state IN ('TRIAL','ACTIVE','GRACE','SUSPENDED','CANCELED')),
    trial_ends_at timestamptz,
    version bigint NOT NULL CHECK (version > 0),
    payload jsonb NOT NULL,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS commerce_one_live_subscription_per_tenant_uq ON commerce.subscriptions (tenant_id) WHERE state <> 'CANCELED';
CREATE INDEX IF NOT EXISTS commerce_subscriptions_tenant_idx ON commerce.subscriptions (tenant_id, created_at DESC);

CREATE TABLE IF NOT EXISTS commerce.billing_periods (
    id text PRIMARY KEY,
    tenant_id text NOT NULL,
    subscription_id text NOT NULL,
    plan_version_id text NOT NULL,
    start_at timestamptz NOT NULL,
    end_at timestamptz NOT NULL,
    state text NOT NULL CHECK (state IN ('OPEN','CLOSING','CLOSED','INVOICED')),
    version bigint NOT NULL CHECK (version > 0),
    payload jsonb NOT NULL,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    CHECK (start_at < end_at)
);
CREATE INDEX IF NOT EXISTS commerce_periods_tenant_time_idx ON commerce.billing_periods (tenant_id, start_at, end_at);
CREATE UNIQUE INDEX IF NOT EXISTS commerce_one_open_period_per_subscription_uq ON commerce.billing_periods (subscription_id) WHERE state IN ('OPEN','CLOSING');

CREATE TABLE IF NOT EXISTS commerce.commercial_accounts (
    tenant_id text PRIMARY KEY,
    state text NOT NULL CHECK (state IN ('ACTIVE','GRACE','SUSPENDED','CANCELED')),
    version bigint NOT NULL CHECK (version > 0),
    payload jsonb NOT NULL,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL
);

CREATE TABLE IF NOT EXISTS commerce.quota_reservations (
    id text PRIMARY KEY,
    tenant_id text NOT NULL,
    resource text NOT NULL,
    policy_version text NOT NULL,
    reason text NOT NULL DEFAULT '',
    quantity bigint NOT NULL CHECK (quantity > 0),
    state text NOT NULL CHECK (state IN ('RESERVED','COMMITTED','RELEASED','REJECTED')),
    idempotency_key text NOT NULL,
    fingerprint text NOT NULL CHECK (fingerprint ~ '^[0-9a-f]{64}$'),
    expires_at timestamptz NOT NULL,
    version bigint NOT NULL CHECK (version > 0),
    payload jsonb NOT NULL,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    UNIQUE (tenant_id, idempotency_key)
);
CREATE INDEX IF NOT EXISTS commerce_quota_capacity_idx ON commerce.quota_reservations (tenant_id, resource, state, expires_at);

CREATE TABLE IF NOT EXISTS commerce.usage_events (
    id text PRIMARY KEY,
    tenant_id text NOT NULL,
    period_id text NOT NULL,
    resource_type text NOT NULL,
    resource_id text NOT NULL,
    meter text NOT NULL CHECK (meter IN (
        'runtime.unit_seconds','runtime.extra_replica_seconds','build.cpu_seconds',
        'build.memory_gib_seconds','build.docker_vm_seconds','artifact.storage_gib_hours',
        'persistent.storage_gib_hours','database.plan_seconds','object_storage.gib_hours',
        'egress.bytes','logs.ingested_bytes','logs.retained_gib_hours'
    )),
    kind text NOT NULL CHECK (kind IN ('STANDARD','CREDIT','CORRECTION')),
    quantity bigint NOT NULL,
    idempotency_key text NOT NULL,
    occurred_at timestamptz NOT NULL,
    window_start timestamptz NOT NULL,
    window_end timestamptz NOT NULL,
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
    version bigint NOT NULL CHECK (version > 0),
    payload jsonb NOT NULL,
    created_at timestamptz NOT NULL,
    CHECK (window_start < window_end),
    CHECK (kind <> 'STANDARD' OR quantity >= 0),
    UNIQUE (tenant_id, idempotency_key)
);
CREATE INDEX IF NOT EXISTS commerce_usage_period_idx ON commerce.usage_events (tenant_id, period_id, occurred_at, id);
CREATE INDEX IF NOT EXISTS commerce_usage_resource_idx ON commerce.usage_events (tenant_id, resource_type, resource_id, meter, window_start, window_end);

CREATE TABLE IF NOT EXISTS commerce.idempotency (
    tenant_id text NOT NULL,
    scope text NOT NULL,
    key text NOT NULL,
    fingerprint text NOT NULL,
    resource_id text NOT NULL,
    created_at timestamptz NOT NULL,
    PRIMARY KEY (tenant_id, scope, key)
);

CREATE TABLE IF NOT EXISTS commerce.outbox (
    id text PRIMARY KEY,
    tenant_id text NOT NULL,
    topic text NOT NULL,
    aggregate_id text NOT NULL,
    payload jsonb NOT NULL,
    created_at timestamptz NOT NULL,
    published_at timestamptz
);
CREATE INDEX IF NOT EXISTS commerce_outbox_pending_idx ON commerce.outbox (created_at, id) WHERE published_at IS NULL;

CREATE TABLE IF NOT EXISTS commerce.audit (
    id text PRIMARY KEY,
    tenant_id text NOT NULL,
    actor_id text NOT NULL,
    action text NOT NULL,
    resource_type text NOT NULL,
    resource_id text NOT NULL,
    data jsonb NOT NULL,
    created_at timestamptz NOT NULL
);
CREATE INDEX IF NOT EXISTS commerce_audit_tenant_idx ON commerce.audit (tenant_id, created_at, id);

CREATE TABLE IF NOT EXISTS commerce.reconciliation_alerts (
    id text PRIMARY KEY,
    tenant_id text NOT NULL,
    period_id text NOT NULL,
    meter text NOT NULL,
    resource_id text NOT NULL,
    reason text NOT NULL,
    drift bigint NOT NULL,
    created_at timestamptz NOT NULL
);
CREATE INDEX IF NOT EXISTS commerce_alerts_tenant_period_idx ON commerce.reconciliation_alerts (tenant_id, period_id, created_at);
