CREATE SCHEMA IF NOT EXISTS infrastructure;

CREATE TABLE IF NOT EXISTS infrastructure.plans (
    id text PRIMARY KEY,
    tenant_id text NOT NULL,
    project_id text NOT NULL,
    workspace_id text NOT NULL,
    source_sha text NOT NULL,
    plan_hash text NOT NULL CHECK (plan_hash ~ '^sha256:[0-9a-f]{64}$'),
    state_generation bigint NOT NULL CHECK (state_generation >= 0),
    changes jsonb NOT NULL CHECK (jsonb_typeof(changes) = 'array'),
    destructive boolean NOT NULL,
    requires_approval boolean NOT NULL,
    estimate_version text NOT NULL,
    estimate_fingerprint text NOT NULL CHECK (estimate_fingerprint ~ '^sha256:[0-9a-f]{64}$'),
    artifact_digest text NOT NULL CHECK (artifact_digest ~ '^sha256:[0-9a-f]{64}$'),
    target text NOT NULL,
    idempotency_key text NOT NULL,
    idempotency_fingerprint text NOT NULL CHECK (idempotency_fingerprint ~ '^sha256:[0-9a-f]{64}$'),
    estimate jsonb NOT NULL CHECK (jsonb_typeof(estimate) = 'object'),
    reservation jsonb NOT NULL CHECK (jsonb_typeof(reservation) = 'object'),
    apply_started_at timestamptz,
    created_at timestamptz NOT NULL,
    version bigint NOT NULL CHECK (version > 0),
    UNIQUE (tenant_id, project_id, idempotency_key)
);

CREATE TABLE IF NOT EXISTS infrastructure.approval_grants (
    id text PRIMARY KEY,
    tenant_id text NOT NULL,
    project_id text NOT NULL,
    plan_id text NOT NULL REFERENCES infrastructure.plans(id) ON DELETE RESTRICT,
    plan_hash text NOT NULL CHECK (plan_hash ~ '^sha256:[0-9a-f]{64}$'),
    estimate_version text NOT NULL,
    reservation_id text NOT NULL,
    target text NOT NULL,
    actor_id text NOT NULL,
    approver_user_id text NOT NULL,
    created_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL,
    consumed_at timestamptz
);

CREATE INDEX IF NOT EXISTS infrastructure_approval_active_idx
    ON infrastructure.approval_grants(plan_id, expires_at)
    WHERE consumed_at IS NULL;

CREATE TABLE IF NOT EXISTS infrastructure.audit (
    sequence_id bigserial PRIMARY KEY,
    tenant_id text NOT NULL,
    project_id text NOT NULL,
    actor_id text NOT NULL,
    action text NOT NULL,
    resource_id text NOT NULL,
    metadata jsonb NOT NULL CHECK (jsonb_typeof(metadata) = 'object'),
    occurred_at timestamptz NOT NULL
);

CREATE OR REPLACE FUNCTION infrastructure.protect_plan_identity()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF (to_jsonb(OLD) - ARRAY['apply_started_at','version']) <>
       (to_jsonb(NEW) - ARRAY['apply_started_at','version'])
       OR OLD.apply_started_at IS NOT NULL OR NEW.apply_started_at IS NULL
       OR NEW.version <> OLD.version + 1 THEN
        RAISE EXCEPTION 'infrastructure plan identity is immutable' USING ERRCODE = '55000';
    END IF;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS infrastructure_plan_identity_immutable ON infrastructure.plans;
CREATE TRIGGER infrastructure_plan_identity_immutable
BEFORE UPDATE ON infrastructure.plans
FOR EACH ROW EXECUTE FUNCTION infrastructure.protect_plan_identity();

CREATE OR REPLACE FUNCTION infrastructure.protect_approval_identity()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF (to_jsonb(OLD) - 'consumed_at') <> (to_jsonb(NEW) - 'consumed_at')
       OR OLD.consumed_at IS NOT NULL OR NEW.consumed_at IS NULL THEN
        RAISE EXCEPTION 'infrastructure approval identity is immutable' USING ERRCODE = '55000';
    END IF;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS infrastructure_approval_identity_immutable ON infrastructure.approval_grants;
CREATE TRIGGER infrastructure_approval_identity_immutable
BEFORE UPDATE ON infrastructure.approval_grants
FOR EACH ROW EXECUTE FUNCTION infrastructure.protect_approval_identity();

CREATE OR REPLACE FUNCTION infrastructure.reject_append_only_mutation()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'infrastructure audit is append-only' USING ERRCODE = '23000';
END;
$$;

DROP TRIGGER IF EXISTS infrastructure_audit_append_only ON infrastructure.audit;
CREATE TRIGGER infrastructure_audit_append_only
BEFORE UPDATE OR DELETE ON infrastructure.audit
FOR EACH ROW EXECUTE FUNCTION infrastructure.reject_append_only_mutation();
