CREATE OR REPLACE FUNCTION source.enforce_repository_provider_identity() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF OLD.provider_project_id IS NOT NULL
       AND NEW.provider_project_id IS DISTINCT FROM OLD.provider_project_id THEN
        RAISE EXCEPTION 'repository provider_project_id is immutable once assigned' USING ERRCODE = '55000';
    END IF;
    RETURN NEW;
END;
$$;
DROP TRIGGER IF EXISTS source_repository_provider_identity ON source.repositories;
CREATE TRIGGER source_repository_provider_identity BEFORE UPDATE OF provider_project_id ON source.repositories
FOR EACH ROW EXECUTE FUNCTION source.enforce_repository_provider_identity();
