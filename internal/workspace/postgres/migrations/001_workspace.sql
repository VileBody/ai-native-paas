CREATE SCHEMA IF NOT EXISTS workspace;

CREATE TABLE IF NOT EXISTS workspace.workspaces (
    id text PRIMARY KEY,
    tenant_id text NOT NULL,
    project_id text NOT NULL,
    task_id text NOT NULL,
    spec jsonb NOT NULL CHECK (jsonb_typeof(spec) = 'object'),
    state text NOT NULL CHECK (state IN ('PROVISIONING','READY','BUSY','DESTROYING','DESTROYED','FAILED')),
    idempotency_key text NOT NULL,
    request_hash text NOT NULL,
    correlation_id text NOT NULL UNIQUE,
    provider_vm_id text NOT NULL DEFAULT '',
    provider_disk_ids jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(provider_disk_ids) = 'array'),
    provider_fingerprint text NOT NULL DEFAULT '',
    last_error text NOT NULL DEFAULT '',
    expires_at timestamptz NOT NULL,
    created_by text NOT NULL,
    updated_by text NOT NULL,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    version bigint NOT NULL CHECK (version > 0),
    reconcile_owner text NOT NULL DEFAULT '',
    reconcile_lease_until timestamptz,
    UNIQUE (tenant_id, project_id, task_id),
    UNIQUE (tenant_id, project_id, idempotency_key)
);

CREATE INDEX IF NOT EXISTS workspaces_expiry_idx ON workspace.workspaces(expires_at)
    WHERE state NOT IN ('DESTROYED','DESTROYING');
CREATE INDEX IF NOT EXISTS workspaces_reconcile_idx ON workspace.workspaces(state, reconcile_lease_until)
    WHERE state IN ('PROVISIONING','DESTROYING');

CREATE TABLE IF NOT EXISTS workspace.commands (
    id text PRIMARY KEY,
    tenant_id text NOT NULL,
    project_id text NOT NULL,
    task_id text NOT NULL,
    workspace_id text NOT NULL REFERENCES workspace.workspaces(id) ON DELETE RESTRICT,
    spec jsonb NOT NULL CHECK (jsonb_typeof(spec) = 'object'),
    kind text NOT NULL,
    serialization_key text NOT NULL DEFAULT '',
    credential_leases jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(credential_leases) = 'array'),
    actor_id text NOT NULL,
    idempotency_key text NOT NULL,
    request_hash text NOT NULL,
    state text NOT NULL CHECK (state IN ('QUEUED','RUNNING','SUCCEEDED','FAILED','CANCELED','TIMED_OUT')),
    agent_session_id text NOT NULL DEFAULT '',
    execution_vm_id text NOT NULL DEFAULT '',
    exit_code integer,
    started_at timestamptz,
    finished_at timestamptz,
    usage_started_at timestamptz,
    usage_finished_at timestamptz,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    version bigint NOT NULL CHECK (version > 0),
    UNIQUE (tenant_id, project_id, idempotency_key)
);

CREATE INDEX IF NOT EXISTS commands_timeout_idx ON workspace.commands(started_at)
    WHERE state = 'RUNNING';

CREATE TABLE IF NOT EXISTS workspace.serialization_locks (
    project_id text NOT NULL,
    serialization_key text NOT NULL,
    command_id text NOT NULL REFERENCES workspace.commands(id) ON DELETE RESTRICT,
    acquired_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (project_id, serialization_key)
);

CREATE TABLE IF NOT EXISTS workspace.outbox (
    event_id bigserial PRIMARY KEY,
    aggregate_type text NOT NULL,
    aggregate_id text NOT NULL,
    aggregate_version bigint NOT NULL,
    event_type text NOT NULL,
    payload jsonb NOT NULL CHECK (jsonb_typeof(payload) = 'object'),
    created_at timestamptz NOT NULL DEFAULT now(),
    published_at timestamptz,
    UNIQUE (aggregate_type, aggregate_id, aggregate_version, event_type)
);

CREATE INDEX IF NOT EXISTS workspace_outbox_pending_idx ON workspace.outbox(event_id)
    WHERE published_at IS NULL;

CREATE TABLE IF NOT EXISTS workspace.audit (
    sequence_id bigserial PRIMARY KEY,
    tenant_id text NOT NULL,
    project_id text NOT NULL,
    actor_id text NOT NULL,
    action text NOT NULL,
    resource_type text NOT NULL,
    resource_id text NOT NULL,
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata) = 'object'),
    occurred_at timestamptz NOT NULL
);

CREATE OR REPLACE FUNCTION workspace.reject_append_only_mutation()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION '% is append-only', TG_TABLE_NAME USING ERRCODE = '23000';
END;
$$;

CREATE OR REPLACE FUNCTION workspace.guard_outbox_delivery_update()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'outbox is append-only' USING ERRCODE = '23000';
    END IF;
    IF OLD.published_at IS NOT NULL OR NEW.published_at IS NULL
       OR (to_jsonb(OLD) - 'published_at') <> (to_jsonb(NEW) - 'published_at') THEN
        RAISE EXCEPTION 'only the first outbox delivery timestamp may be recorded' USING ERRCODE = '23000';
    END IF;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS workspace_outbox_append_only ON workspace.outbox;
CREATE TRIGGER workspace_outbox_append_only
BEFORE UPDATE OR DELETE ON workspace.outbox
FOR EACH ROW EXECUTE FUNCTION workspace.guard_outbox_delivery_update();

DROP TRIGGER IF EXISTS workspace_audit_append_only ON workspace.audit;
CREATE TRIGGER workspace_audit_append_only
BEFORE UPDATE OR DELETE ON workspace.audit
FOR EACH ROW EXECUTE FUNCTION workspace.reject_append_only_mutation();
