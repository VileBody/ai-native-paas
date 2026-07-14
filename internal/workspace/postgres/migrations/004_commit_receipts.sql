CREATE TABLE IF NOT EXISTS workspace.commit_receipts (
    command_id text PRIMARY KEY REFERENCES workspace.commands(id) ON DELETE RESTRICT,
    tenant_id text NOT NULL,
    project_id text NOT NULL,
    workspace_id text NOT NULL REFERENCES workspace.workspaces(id) ON DELETE RESTRICT,
    task_id text NOT NULL,
    actor_id text NOT NULL,
    receipt jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CHECK (jsonb_typeof(receipt) = 'object')
);

CREATE INDEX IF NOT EXISTS commit_receipts_scope_idx
    ON workspace.commit_receipts(tenant_id, project_id, command_id);
