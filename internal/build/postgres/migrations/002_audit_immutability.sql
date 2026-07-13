CREATE OR REPLACE FUNCTION build.reject_audit_mutation() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'build.audit is append-only' USING ERRCODE = '55000';
END;
$$;
DROP TRIGGER IF EXISTS build_audit_no_update ON build.audit;
CREATE TRIGGER build_audit_no_update BEFORE UPDATE OR DELETE ON build.audit
FOR EACH ROW EXECUTE FUNCTION build.reject_audit_mutation();
