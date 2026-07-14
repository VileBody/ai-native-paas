ALTER TABLE workspace.workspaces
    ADD COLUMN IF NOT EXISTS provider_firewall_group_ids jsonb NOT NULL DEFAULT '[]'::jsonb;

ALTER TABLE workspace.workspaces
    DROP CONSTRAINT IF EXISTS workspaces_provider_firewall_group_ids_check;

ALTER TABLE workspace.workspaces
    ADD CONSTRAINT workspaces_provider_firewall_group_ids_check
    CHECK (jsonb_typeof(provider_firewall_group_ids) = 'array');

ALTER TABLE workspace.commands
    ADD COLUMN IF NOT EXISTS cancel_requested_at timestamptz;

DROP INDEX IF EXISTS workspace.commands_timeout_idx;
CREATE INDEX commands_timeout_idx ON workspace.commands(started_at)
    WHERE state = 'RUNNING' AND cancel_requested_at IS NULL;

