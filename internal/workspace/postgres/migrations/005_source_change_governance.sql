CREATE TABLE IF NOT EXISTS workspace.source_change_plans (
    id text PRIMARY KEY,
    tenant_id text NOT NULL,
    project_id text NOT NULL,
    workspace_id text NOT NULL REFERENCES workspace.workspaces(id) ON DELETE RESTRICT,
    repository_id text NOT NULL,
    base_sha text NOT NULL,
    target_branch text NOT NULL,
    actor_id text NOT NULL,
    task_id text NOT NULL,
    files jsonb NOT NULL,
    plan_hash text NOT NULL,
    requires_approval boolean NOT NULL,
    idempotency_key text NOT NULL,
    request_fingerprint text NOT NULL,
    authorized_idempotency_key text NOT NULL DEFAULT '',
    authorization_fingerprint text NOT NULL DEFAULT '',
    authorized_at timestamptz,
    created_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL,
    UNIQUE (tenant_id, project_id, idempotency_key),
    CHECK (jsonb_typeof(files) = 'array'),
    CHECK (expires_at > created_at)
);

CREATE TABLE IF NOT EXISTS workspace.source_approval_grants (
    id text PRIMARY KEY,
    tenant_id text NOT NULL,
    project_id text NOT NULL,
    plan_id text NOT NULL REFERENCES workspace.source_change_plans(id) ON DELETE RESTRICT,
    plan_hash text NOT NULL,
    workspace_id text NOT NULL,
    repository_id text NOT NULL,
    target_branch text NOT NULL,
    actor_id text NOT NULL,
    approver_user_id text NOT NULL,
    created_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL,
    consumed_at timestamptz,
    CHECK (expires_at > created_at),
    CHECK (consumed_at IS NULL OR consumed_at >= created_at)
);

CREATE INDEX IF NOT EXISTS source_change_plans_scope_idx
    ON workspace.source_change_plans(tenant_id, project_id, id);
CREATE INDEX IF NOT EXISTS source_approval_active_idx
    ON workspace.source_approval_grants(tenant_id, project_id, plan_id, actor_id, expires_at)
    WHERE consumed_at IS NULL;
CREATE UNIQUE INDEX IF NOT EXISTS source_approval_one_unconsumed_idx
    ON workspace.source_approval_grants(plan_id)
    WHERE consumed_at IS NULL;
