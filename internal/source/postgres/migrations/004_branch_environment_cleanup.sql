ALTER TABLE source.branch_heads
    ADD COLUMN IF NOT EXISTS environment_id text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS deleted_at timestamptz;

CREATE UNIQUE INDEX IF NOT EXISTS source_branch_environment_identity_idx
    ON source.branch_heads (repository_id, environment_id)
    WHERE environment_id <> '';
