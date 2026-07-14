CREATE TABLE IF NOT EXISTS workspace.agent_sessions (
    id text PRIMARY KEY,
    tenant_id text NOT NULL,
    project_id text NOT NULL,
    workspace_id text NOT NULL REFERENCES workspace.workspaces(id) ON DELETE RESTRICT,
    task_id text NOT NULL,
    agent_id text NOT NULL,
    vm_id text NOT NULL,
    certificate_id text NOT NULL UNIQUE,
    connected_at timestamptz NOT NULL,
    last_seen_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL,
    closed_at timestamptz,
    close_reason text NOT NULL DEFAULT '',
    version bigint NOT NULL CHECK (version > 0),
    CHECK (expires_at > connected_at),
    CHECK ((closed_at IS NULL AND close_reason = '') OR (closed_at IS NOT NULL AND close_reason <> ''))
);

CREATE UNIQUE INDEX IF NOT EXISTS agent_sessions_one_open_workspace_idx
    ON workspace.agent_sessions(workspace_id) WHERE closed_at IS NULL;
CREATE INDEX IF NOT EXISTS agent_sessions_active_idx
    ON workspace.agent_sessions(workspace_id, vm_id, expires_at, last_seen_at)
    WHERE closed_at IS NULL;

CREATE TABLE IF NOT EXISTS workspace.agent_messages (
    id text PRIMARY KEY,
    workspace_id text NOT NULL REFERENCES workspace.workspaces(id) ON DELETE RESTRICT,
    command_id text NOT NULL,
    kind text NOT NULL CHECK (kind IN ('EXEC','CANCEL')),
    payload jsonb NOT NULL CHECK (jsonb_typeof(payload) = 'object'),
    payload_hash text NOT NULL,
    state text NOT NULL CHECK (state IN ('QUEUED','DELIVERED','ACKED','REJECTED')),
    session_id text NOT NULL DEFAULT '',
    vm_id text NOT NULL DEFAULT '',
    delivery_attempts bigint NOT NULL DEFAULT 0 CHECK (delivery_attempts >= 0),
    delivery_lease_until timestamptz,
    created_at timestamptz NOT NULL,
    delivered_at timestamptz,
    acknowledged_at timestamptz,
    UNIQUE (workspace_id, command_id, kind),
    CHECK (
        (state = 'QUEUED' AND session_id = '' AND vm_id = '' AND delivered_at IS NULL AND acknowledged_at IS NULL)
        OR (state = 'DELIVERED' AND session_id <> '' AND vm_id <> '' AND delivered_at IS NOT NULL AND delivery_lease_until IS NOT NULL AND acknowledged_at IS NULL)
        OR (state IN ('ACKED','REJECTED') AND session_id <> '' AND vm_id <> '' AND delivered_at IS NOT NULL AND delivery_lease_until IS NULL AND acknowledged_at IS NOT NULL)
    )
);

CREATE INDEX IF NOT EXISTS agent_messages_delivery_idx
    ON workspace.agent_messages(workspace_id, created_at, id)
    WHERE state IN ('QUEUED','DELIVERED');
