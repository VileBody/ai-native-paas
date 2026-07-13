CREATE SCHEMA IF NOT EXISTS attachments;

CREATE TABLE attachments.secret_sets (
    id text PRIMARY KEY,
    tenant_id text NOT NULL,
    application_id text NOT NULL,
    environment_id text NOT NULL,
    provider_path text NOT NULL,
    version bigint NOT NULL CHECK (version > 0),
    payload jsonb NOT NULL,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    UNIQUE (tenant_id, environment_id)
);

CREATE TABLE attachments.secrets (
    id text PRIMARY KEY,
    tenant_id text NOT NULL,
    secret_set_id text NOT NULL REFERENCES attachments.secret_sets(id),
    application_id text NOT NULL,
    environment_id text NOT NULL,
    name text NOT NULL,
    scope text NOT NULL CHECK (scope IN ('runtime', 'build')),
    phase text NOT NULL CHECK (phase IN ('runtime', 'detect', 'build')),
    provider_ref text NOT NULL,
    provider_version text NOT NULL,
    version bigint NOT NULL CHECK (version > 0),
    expires_at timestamptz,
    deleted boolean NOT NULL DEFAULT false,
    payload jsonb NOT NULL,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    UNIQUE (secret_set_id, name, scope)
);

CREATE TABLE attachments.service_plans (
    id text NOT NULL,
    version bigint NOT NULL CHECK (version > 0),
    service_type text NOT NULL CHECK (service_type IN ('postgresql', 'redis', 's3')),
    provider text NOT NULL,
    provider_plan text NOT NULL,
    provider_mapping_version text NOT NULL,
    enabled boolean NOT NULL,
    payload jsonb NOT NULL,
    created_at timestamptz NOT NULL,
    PRIMARY KEY (id, version)
);

CREATE TABLE attachments.service_instances (
    id text PRIMARY KEY,
    tenant_id text NOT NULL,
    application_id text NOT NULL DEFAULT '',
    environment_id text NOT NULL DEFAULT '',
    name text NOT NULL,
    plan_id text NOT NULL,
    plan_version bigint NOT NULL,
    service_type text NOT NULL,
    state text NOT NULL,
    provider_operation_key text NOT NULL,
    provider_id text NOT NULL DEFAULT '',
    provider_endpoint text NOT NULL DEFAULT '',
    version bigint NOT NULL CHECK (version > 0),
    payload jsonb NOT NULL,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    FOREIGN KEY (plan_id, plan_version) REFERENCES attachments.service_plans(id, version)
);
CREATE UNIQUE INDEX attachments_service_instance_name_active
    ON attachments.service_instances (tenant_id, environment_id, lower(name))
    WHERE state <> 'DELETED';

CREATE TABLE attachments.service_bindings (
    id text PRIMARY KEY,
    tenant_id text NOT NULL,
    application_id text NOT NULL,
    environment_id text NOT NULL,
    instance_id text NOT NULL REFERENCES attachments.service_instances(id),
    state text NOT NULL,
    provider_credential_id text NOT NULL DEFAULT '',
    version bigint NOT NULL CHECK (version > 0),
    payload jsonb NOT NULL,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL
);
CREATE UNIQUE INDEX attachments_binding_active
    ON attachments.service_bindings (tenant_id, environment_id, instance_id)
    WHERE state <> 'REVOKED';

CREATE TABLE attachments.domain_claims (
    id text PRIMARY KEY,
    tenant_id text NOT NULL,
    application_id text NOT NULL,
    environment_id text NOT NULL,
    hostname text NOT NULL,
    generated boolean NOT NULL,
    state text NOT NULL,
    challenge_name text NOT NULL DEFAULT '',
    challenge_value text NOT NULL DEFAULT '',
    version bigint NOT NULL CHECK (version > 0),
    payload jsonb NOT NULL,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL
);
CREATE UNIQUE INDEX attachments_domain_claim_active
    ON attachments.domain_claims (lower(hostname))
    WHERE state <> 'RELEASED';

CREATE TABLE attachments.snapshots (
    id text PRIMARY KEY,
    tenant_id text NOT NULL,
    application_id text NOT NULL,
    environment_id text NOT NULL,
    version bigint NOT NULL CHECK (version > 0),
    content_hash text NOT NULL,
    secret_set_ref text NOT NULL,
    payload jsonb NOT NULL,
    created_at timestamptz NOT NULL,
    UNIQUE (tenant_id, environment_id, version),
    UNIQUE (tenant_id, environment_id, content_hash)
);

CREATE TABLE attachments.idempotency (
    tenant_id text NOT NULL,
    scope text NOT NULL,
    key text NOT NULL,
    request_hash text NOT NULL,
    resource_id text NOT NULL,
    created_at timestamptz NOT NULL,
    PRIMARY KEY (tenant_id, scope, key)
);

CREATE TABLE attachments.outbox (
    id text PRIMARY KEY,
    tenant_id text NOT NULL,
    topic text NOT NULL,
    aggregate_id text NOT NULL,
    payload jsonb NOT NULL,
    created_at timestamptz NOT NULL
);

CREATE TABLE attachments.audit (
    id text PRIMARY KEY,
    tenant_id text NOT NULL,
    actor_id text NOT NULL,
    action text NOT NULL,
    resource_type text NOT NULL,
    resource_id text NOT NULL,
    data jsonb NOT NULL,
    created_at timestamptz NOT NULL
);

CREATE INDEX attachments_outbox_created ON attachments.outbox(created_at, id);
CREATE INDEX attachments_audit_resource ON attachments.audit(tenant_id, resource_type, resource_id, created_at);
