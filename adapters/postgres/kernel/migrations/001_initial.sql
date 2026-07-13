CREATE TABLE kernel.organizations (
    id          text PRIMARY KEY,
    name        text NOT NULL CHECK (length(btrim(name)) > 0),
    slug        text NOT NULL UNIQUE CHECK (length(btrim(slug)) > 0),
    version     bigint NOT NULL CHECK (version > 0),
    created_at  timestamptz NOT NULL,
    updated_at  timestamptz NOT NULL
);

CREATE TABLE kernel.memberships (
    organization_id text NOT NULL REFERENCES kernel.organizations(id) ON DELETE CASCADE,
    principal_id    text NOT NULL,
    role            text NOT NULL CHECK (role IN ('owner', 'admin', 'developer', 'viewer')),
    state           text NOT NULL CHECK (state IN ('INVITED', 'ACTIVE', 'SUSPENDED', 'REMOVED')),
    invited_at      timestamptz NOT NULL,
    accepted_at     timestamptz,
    updated_at      timestamptz NOT NULL,
    version         bigint NOT NULL CHECK (version > 0),
    PRIMARY KEY (organization_id, principal_id)
);

CREATE TABLE kernel.operations (
    id              text PRIMARY KEY,
    tenant_id       text NOT NULL DEFAULT '',
    kind            text NOT NULL,
    state           text NOT NULL CHECK (state IN ('PENDING', 'RUNNING', 'WAITING_EXTERNAL', 'SUCCEEDED', 'FAILED', 'CANCELED')),
    correlation_id  text NOT NULL,
    causation_id    text NOT NULL DEFAULT '',
    result          jsonb NOT NULL DEFAULT '{}'::jsonb,
    version         bigint NOT NULL CHECK (version > 0),
    created_at      timestamptz NOT NULL,
    updated_at      timestamptz NOT NULL
);

CREATE TABLE kernel.idempotency_records (
    scope           text NOT NULL,
    idempotency_key text NOT NULL,
    fingerprint     text NOT NULL,
    status          text NOT NULL CHECK (status IN ('STARTED', 'COMPLETED')),
    response        jsonb,
    operation_id    text NOT NULL DEFAULT '',
    created_at      timestamptz NOT NULL,
    updated_at      timestamptz NOT NULL,
    PRIMARY KEY (scope, idempotency_key)
);

CREATE TABLE kernel.outbox_events (
    event_id       text PRIMARY KEY,
    envelope       jsonb NOT NULL,
    state          text NOT NULL CHECK (state IN ('PENDING', 'DISPATCHING', 'PUBLISHED')),
    attempts       integer NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    lease_until    timestamptz,
    last_error     text NOT NULL DEFAULT '',
    created_at     timestamptz NOT NULL,
    published_at   timestamptz
);

CREATE TABLE kernel.inbox_events (
    event_id       text PRIMARY KEY,
    fingerprint    text NOT NULL,
    processed_at   timestamptz NOT NULL
);

CREATE TABLE kernel.audit_records (
    audit_id        text PRIMARY KEY,
    tenant_id       text NOT NULL DEFAULT '',
    actor           jsonb NOT NULL,
    action          text NOT NULL,
    resource        jsonb NOT NULL,
    correlation_id  text NOT NULL,
    outcome         text NOT NULL CHECK (outcome IN ('SUCCEEDED', 'DENIED', 'FAILED')),
    error_code      text NOT NULL DEFAULT '',
    metadata        jsonb NOT NULL DEFAULT '{}'::jsonb,
    occurred_at     timestamptz NOT NULL
);

CREATE OR REPLACE FUNCTION kernel.reject_audit_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION 'kernel.audit_records is append-only' USING ERRCODE = '55000';
END;
$$;

CREATE TRIGGER audit_records_reject_update
BEFORE UPDATE ON kernel.audit_records
FOR EACH ROW EXECUTE FUNCTION kernel.reject_audit_mutation();

CREATE TRIGGER audit_records_reject_delete
BEFORE DELETE ON kernel.audit_records
FOR EACH ROW EXECUTE FUNCTION kernel.reject_audit_mutation();
