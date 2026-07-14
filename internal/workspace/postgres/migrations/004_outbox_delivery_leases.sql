ALTER TABLE workspace.outbox
    ADD COLUMN IF NOT EXISTS delivery_owner text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS delivery_lease_until timestamptz,
    ADD COLUMN IF NOT EXISTS delivery_attempts bigint NOT NULL DEFAULT 0;

ALTER TABLE workspace.outbox
    DROP CONSTRAINT IF EXISTS workspace_outbox_delivery_attempts_check;
ALTER TABLE workspace.outbox
    ADD CONSTRAINT workspace_outbox_delivery_attempts_check
    CHECK (delivery_attempts >= 0);

ALTER TABLE workspace.outbox
    DROP CONSTRAINT IF EXISTS workspace_outbox_delivery_lease_check;
ALTER TABLE workspace.outbox
    ADD CONSTRAINT workspace_outbox_delivery_lease_check
    CHECK (
        (delivery_owner = '' AND delivery_lease_until IS NULL)
        OR (delivery_owner <> '' AND delivery_lease_until IS NOT NULL)
    );

DROP INDEX IF EXISTS workspace.workspace_outbox_pending_idx;
CREATE INDEX workspace_outbox_pending_idx
    ON workspace.outbox(event_id, delivery_lease_until)
    WHERE published_at IS NULL;

CREATE OR REPLACE FUNCTION workspace.guard_outbox_delivery_update()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'outbox is append-only' USING ERRCODE = '23000';
    END IF;
    IF OLD.published_at IS NOT NULL
       OR OLD.event_id <> NEW.event_id
       OR OLD.aggregate_type <> NEW.aggregate_type
       OR OLD.aggregate_id <> NEW.aggregate_id
       OR OLD.aggregate_version <> NEW.aggregate_version
       OR OLD.event_type <> NEW.event_type
       OR OLD.payload <> NEW.payload
       OR OLD.created_at <> NEW.created_at
       OR NEW.delivery_attempts < OLD.delivery_attempts THEN
        RAISE EXCEPTION 'outbox event identity and payload are immutable' USING ERRCODE = '23000';
    END IF;
    RETURN NEW;
END;
$$;
