ALTER TABLE infrastructure.plans
    ADD COLUMN IF NOT EXISTS apply_idempotency_key text,
    ADD COLUMN IF NOT EXISTS apply_authorization_fingerprint text
        CHECK (apply_authorization_fingerprint IS NULL OR apply_authorization_fingerprint ~ '^sha256:[0-9a-f]{64}$');

-- Applies authorized before this migration cannot be replayed safely because
-- the original request binding was not persisted. Give them a deliberately
-- unmatchable binding so the completeness invariant holds and every retry is
-- rejected as a conflict instead of consuming another approval.
UPDATE infrastructure.plans
SET apply_idempotency_key = 'migration-legacy/' || id,
    apply_authorization_fingerprint = 'sha256:' || repeat('0', 64)
WHERE apply_started_at IS NOT NULL
  AND apply_idempotency_key IS NULL
  AND apply_authorization_fingerprint IS NULL;

ALTER TABLE infrastructure.plans
    ADD CONSTRAINT infrastructure_apply_binding_complete CHECK (
        (apply_started_at IS NULL AND apply_idempotency_key IS NULL AND apply_authorization_fingerprint IS NULL)
        OR
        (apply_started_at IS NOT NULL AND apply_idempotency_key IS NOT NULL AND apply_authorization_fingerprint IS NOT NULL)
    );

CREATE OR REPLACE FUNCTION infrastructure.protect_plan_identity()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF (to_jsonb(OLD) - ARRAY['apply_started_at','apply_idempotency_key','apply_authorization_fingerprint','version']) <>
       (to_jsonb(NEW) - ARRAY['apply_started_at','apply_idempotency_key','apply_authorization_fingerprint','version'])
       OR OLD.apply_started_at IS NOT NULL OR NEW.apply_started_at IS NULL
       OR OLD.apply_idempotency_key IS NOT NULL OR NEW.apply_idempotency_key IS NULL
       OR OLD.apply_authorization_fingerprint IS NOT NULL OR NEW.apply_authorization_fingerprint IS NULL
       OR NEW.version <> OLD.version + 1 THEN
        RAISE EXCEPTION 'infrastructure plan identity is immutable' USING ERRCODE = '55000';
    END IF;
    RETURN NEW;
END;
$$;
