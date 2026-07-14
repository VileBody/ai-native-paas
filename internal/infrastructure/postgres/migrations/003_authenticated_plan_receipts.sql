CREATE TABLE IF NOT EXISTS infrastructure.plan_receipts (
    command_id text PRIMARY KEY,
    tenant_id text NOT NULL,
    project_id text NOT NULL,
    workspace_id text NOT NULL,
    task_id text NOT NULL,
    actor_id text NOT NULL,
    artifact_digest text NOT NULL CHECK (artifact_digest ~ '^sha256:[0-9a-f]{64}$'),
    plan_json jsonb NOT NULL CHECK (
        jsonb_typeof(plan_json) = 'object'
        AND jsonb_typeof(plan_json->'resource_changes') = 'array'
    ),
    captured_at timestamptz NOT NULL,
    received_at timestamptz NOT NULL DEFAULT clock_timestamp()
);

CREATE INDEX IF NOT EXISTS infrastructure_plan_receipts_scope_idx
    ON infrastructure.plan_receipts(tenant_id, project_id, workspace_id, command_id);

DROP TRIGGER IF EXISTS infrastructure_plan_receipt_immutable ON infrastructure.plan_receipts;
CREATE TRIGGER infrastructure_plan_receipt_immutable
BEFORE UPDATE OR DELETE ON infrastructure.plan_receipts
FOR EACH ROW EXECUTE FUNCTION infrastructure.reject_append_only_mutation();
