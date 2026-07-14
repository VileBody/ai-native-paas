ALTER TABLE commerce.quota_reservations
    ADD COLUMN IF NOT EXISTS project_id text NOT NULL DEFAULT '';

ALTER TABLE commerce.quota_reservations
    DROP CONSTRAINT IF EXISTS quota_reservations_state_check;

ALTER TABLE commerce.quota_reservations
    ADD CONSTRAINT quota_reservations_state_check
    CHECK (state IN ('RESERVED','COMMITTED','SETTLED','RELEASED','REJECTED'));

CREATE INDEX IF NOT EXISTS commerce_quota_project_capacity_idx
    ON commerce.quota_reservations (tenant_id, project_id, resource, state, expires_at);

CREATE OR REPLACE FUNCTION commerce.guard_quota() RETURNS trigger AS $$
BEGIN
    IF NEW.id <> OLD.id OR NEW.tenant_id <> OLD.tenant_id OR NEW.project_id <> OLD.project_id OR
       NEW.resource <> OLD.resource OR NEW.policy_version <> OLD.policy_version OR
       NEW.quantity <> OLD.quantity OR NEW.idempotency_key <> OLD.idempotency_key OR NEW.fingerprint <> OLD.fingerprint OR
       NEW.expires_at <> OLD.expires_at OR NEW.created_at <> OLD.created_at THEN
        RAISE EXCEPTION 'quota reservation request is immutable' USING ERRCODE = '55000';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE OR REPLACE FUNCTION commerce.guard_quota_project_payload_consistency() RETURNS trigger AS $$
BEGIN
    IF COALESCE(NEW.payload->>'ProjectID','') IS DISTINCT FROM NEW.project_id THEN
        RAISE EXCEPTION 'quota reservation project payload drift' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS commerce_quota_project_payload_guard ON commerce.quota_reservations;
CREATE TRIGGER commerce_quota_project_payload_guard
    BEFORE INSERT OR UPDATE ON commerce.quota_reservations
    FOR EACH ROW EXECUTE FUNCTION commerce.guard_quota_project_payload_consistency();
