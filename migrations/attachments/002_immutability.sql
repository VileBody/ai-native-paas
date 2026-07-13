CREATE OR REPLACE FUNCTION attachments.reject_append_only_mutation()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION '% is append-only and immutable', TG_TABLE_SCHEMA || '.' || TG_TABLE_NAME
        USING ERRCODE = '55000';
END;
$$;

CREATE TRIGGER attachments_snapshots_append_only
    BEFORE UPDATE OR DELETE ON attachments.snapshots
    FOR EACH ROW EXECUTE FUNCTION attachments.reject_append_only_mutation();
CREATE TRIGGER attachments_audit_append_only
    BEFORE UPDATE OR DELETE ON attachments.audit
    FOR EACH ROW EXECUTE FUNCTION attachments.reject_append_only_mutation();
CREATE TRIGGER attachments_outbox_append_only
    BEFORE UPDATE OR DELETE ON attachments.outbox
    FOR EACH ROW EXECUTE FUNCTION attachments.reject_append_only_mutation();
CREATE TRIGGER attachments_idempotency_append_only
    BEFORE UPDATE OR DELETE ON attachments.idempotency
    FOR EACH ROW EXECUTE FUNCTION attachments.reject_append_only_mutation();
CREATE TRIGGER attachments_service_plans_immutable
    BEFORE UPDATE OR DELETE ON attachments.service_plans
    FOR EACH ROW EXECUTE FUNCTION attachments.reject_append_only_mutation();

CREATE OR REPLACE FUNCTION attachments.guard_provider_identity()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF OLD.provider_id <> '' AND NEW.provider_id IS DISTINCT FROM OLD.provider_id THEN
        RAISE EXCEPTION 'service provider identity is immutable'
            USING ERRCODE = '55000';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER attachments_service_provider_identity
    BEFORE UPDATE ON attachments.service_instances
    FOR EACH ROW EXECUTE FUNCTION attachments.guard_provider_identity();
