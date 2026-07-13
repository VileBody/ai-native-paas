CREATE OR REPLACE FUNCTION source.reject_audit_mutation() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'source.audit is append-only' USING ERRCODE = '55000';
END;
$$;
DROP TRIGGER IF EXISTS source_audit_no_update ON source.audit;
CREATE TRIGGER source_audit_no_update BEFORE UPDATE OR DELETE ON source.audit
FOR EACH ROW EXECUTE FUNCTION source.reject_audit_mutation();
