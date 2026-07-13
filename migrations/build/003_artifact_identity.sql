CREATE OR REPLACE FUNCTION build.enforce_artifact_identity() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.tenant_id IS DISTINCT FROM OLD.tenant_id
       OR NEW.build_id IS DISTINCT FROM OLD.build_id
       OR NEW.repository IS DISTINCT FROM OLD.repository
       OR NEW.digest IS DISTINCT FROM OLD.digest
       OR NEW.media_type IS DISTINCT FROM OLD.media_type THEN
        RAISE EXCEPTION 'artifact identity is immutable' USING ERRCODE = '55000';
    END IF;
    RETURN NEW;
END;
$$;
DROP TRIGGER IF EXISTS build_artifact_identity_immutable ON build.artifacts;
CREATE TRIGGER build_artifact_identity_immutable
BEFORE UPDATE OF tenant_id, build_id, repository, digest, media_type ON build.artifacts
FOR EACH ROW EXECUTE FUNCTION build.enforce_artifact_identity();

CREATE OR REPLACE FUNCTION build.enforce_signature_digest() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE artifact_digest text;
BEGIN
    SELECT digest INTO artifact_digest FROM build.artifacts WHERE id = NEW.artifact_id;
    IF artifact_digest IS NULL OR NEW.digest IS DISTINCT FROM artifact_digest THEN
        RAISE EXCEPTION 'signature digest must equal artifact digest' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;
DROP TRIGGER IF EXISTS build_signature_digest_matches_artifact ON build.signature_records;
CREATE TRIGGER build_signature_digest_matches_artifact
BEFORE INSERT OR UPDATE ON build.signature_records
FOR EACH ROW EXECUTE FUNCTION build.enforce_signature_digest();
