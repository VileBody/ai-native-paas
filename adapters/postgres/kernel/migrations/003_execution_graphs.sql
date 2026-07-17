CREATE TABLE kernel.operation_graphs (
    graph_id             text PRIMARY KEY,
    tenant_id            text NOT NULL CHECK (length(btrim(tenant_id)) > 0),
    project_id           text NOT NULL CHECK (length(btrim(project_id)) > 0),
    workspace_id         text NOT NULL DEFAULT '',
    idempotency_key      text NOT NULL CHECK (length(btrim(idempotency_key)) > 0),
    command_fingerprint  text NOT NULL CHECK (length(btrim(command_fingerprint)) > 0),
    graph                 jsonb NOT NULL,
    version               bigint NOT NULL CHECK (version > 0),
    created_at            timestamptz NOT NULL,
    updated_at            timestamptz NOT NULL,
    UNIQUE (tenant_id, project_id, idempotency_key)
);

CREATE INDEX operation_graphs_project_updated_idx
    ON kernel.operation_graphs (tenant_id, project_id, updated_at DESC, graph_id);

CREATE TABLE kernel.execution_audit_records (
    audit_id       text PRIMARY KEY,
    tenant_id      text NOT NULL,
    project_id     text NOT NULL,
    principal      text NOT NULL,
    action         text NOT NULL,
    outcome        text NOT NULL CHECK (outcome IN ('SUCCEEDED', 'DENIED', 'FAILED')),
    error_code     text NOT NULL DEFAULT '',
    metadata       jsonb NOT NULL DEFAULT '{}'::jsonb,
    occurred_at    timestamptz NOT NULL
);

CREATE INDEX execution_audit_project_occurred_idx
    ON kernel.execution_audit_records (tenant_id, project_id, occurred_at, audit_id);

CREATE TRIGGER execution_audit_records_reject_update
BEFORE UPDATE ON kernel.execution_audit_records
FOR EACH ROW EXECUTE FUNCTION kernel.reject_audit_mutation();

CREATE TRIGGER execution_audit_records_reject_delete
BEFORE DELETE ON kernel.execution_audit_records
FOR EACH ROW EXECUTE FUNCTION kernel.reject_audit_mutation();
