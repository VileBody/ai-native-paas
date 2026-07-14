CREATE TABLE attachments.environment_inputs_snapshots (
    id text PRIMARY KEY,
    project_id text NOT NULL,
    environment text NOT NULL,
    digest text NOT NULL,
    payload jsonb NOT NULL,
    created_at timestamptz NOT NULL,
    UNIQUE (project_id, environment, digest)
);

CREATE OR REPLACE FUNCTION attachments.check_environment_inputs_snapshot_payload()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.payload->>'snapshot_id' IS DISTINCT FROM NEW.id
       OR NEW.payload->>'project_id' IS DISTINCT FROM NEW.project_id
       OR NEW.payload->>'environment' IS DISTINCT FROM NEW.environment
       OR NEW.payload->>'digest' IS DISTINCT FROM NEW.digest
       OR (NEW.payload->>'created_at')::timestamptz IS DISTINCT FROM NEW.created_at THEN
        RAISE EXCEPTION 'attachments environment_inputs_snapshots payload drift'
            USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER attachments_environment_inputs_snapshot_payload_consistency
    BEFORE INSERT ON attachments.environment_inputs_snapshots
    FOR EACH ROW EXECUTE FUNCTION attachments.check_environment_inputs_snapshot_payload();

CREATE TRIGGER attachments_environment_inputs_snapshots_append_only
    BEFORE UPDATE OR DELETE ON attachments.environment_inputs_snapshots
    FOR EACH ROW EXECUTE FUNCTION attachments.reject_append_only_mutation();
