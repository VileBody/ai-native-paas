CREATE OR REPLACE FUNCTION runtime.reject_append_only_mutation() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION '% is append-only', TG_TABLE_NAME USING ERRCODE = '55000';
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS runtime_audit_append_only ON runtime.audit;
CREATE TRIGGER runtime_audit_append_only BEFORE UPDATE OR DELETE ON runtime.audit FOR EACH ROW EXECUTE FUNCTION runtime.reject_append_only_mutation();
DROP TRIGGER IF EXISTS runtime_gitops_append_only ON runtime.gitops_commits;
CREATE TRIGGER runtime_gitops_append_only BEFORE UPDATE OR DELETE ON runtime.gitops_commits FOR EACH ROW EXECUTE FUNCTION runtime.reject_append_only_mutation();
DROP TRIGGER IF EXISTS runtime_idempotency_append_only ON runtime.idempotency;
CREATE TRIGGER runtime_idempotency_append_only BEFORE UPDATE OR DELETE ON runtime.idempotency FOR EACH ROW EXECUTE FUNCTION runtime.reject_append_only_mutation();
DROP TRIGGER IF EXISTS runtime_quarantine_append_only ON runtime.quarantine;
CREATE TRIGGER runtime_quarantine_append_only BEFORE UPDATE OR DELETE ON runtime.quarantine FOR EACH ROW EXECUTE FUNCTION runtime.reject_append_only_mutation();

CREATE OR REPLACE FUNCTION runtime.guard_application_identity() RETURNS trigger AS $$
BEGIN
    IF NEW.id <> OLD.id OR NEW.tenant_id <> OLD.tenant_id OR NEW.project_id <> OLD.project_id OR NEW.created_at <> OLD.created_at THEN
        RAISE EXCEPTION 'application identity is immutable' USING ERRCODE = '55000';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
DROP TRIGGER IF EXISTS runtime_application_identity_immutable ON runtime.applications;
CREATE TRIGGER runtime_application_identity_immutable BEFORE UPDATE ON runtime.applications FOR EACH ROW EXECUTE FUNCTION runtime.guard_application_identity();

CREATE OR REPLACE FUNCTION runtime.guard_environment_identity() RETURNS trigger AS $$
BEGIN
    IF NEW.id <> OLD.id OR NEW.tenant_id <> OLD.tenant_id OR NEW.application_id <> OLD.application_id OR NEW.namespace <> OLD.namespace OR NEW.created_at <> OLD.created_at THEN
        RAISE EXCEPTION 'environment identity is immutable' USING ERRCODE = '55000';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
DROP TRIGGER IF EXISTS runtime_environment_identity_immutable ON runtime.environments;
CREATE TRIGGER runtime_environment_identity_immutable BEFORE UPDATE ON runtime.environments FOR EACH ROW EXECUTE FUNCTION runtime.guard_environment_identity();
