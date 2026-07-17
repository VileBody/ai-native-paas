ALTER TABLE build.artifacts
    ADD COLUMN IF NOT EXISTS provenance_digest text NOT NULL DEFAULT '' CHECK (provenance_digest = '' OR provenance_digest ~ '^sha256:[0-9a-f]{64}$'),
    ADD COLUMN IF NOT EXISTS provenance_media_type text NOT NULL DEFAULT '';

CREATE OR REPLACE FUNCTION build.enforce_artifact_state_and_trust() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    -- One transaction may collapse SCANNED -> SIGNED -> RELEASABLE while
    -- committing the immutable trust records atomically. Versions may jump,
    -- but stale or regressing writes are always rejected.
    IF NEW.version <= OLD.version THEN
        RAISE EXCEPTION 'artifact version must advance' USING ERRCODE = '55000';
    END IF;

    IF OLD.sbom_digest <> '' AND (
       NEW.sbom_digest IS DISTINCT FROM OLD.sbom_digest
       OR NEW.sbom_media_type IS DISTINCT FROM OLD.sbom_media_type) THEN
        RAISE EXCEPTION 'artifact SBOM reference is immutable' USING ERRCODE = '55000';
    END IF;
    IF (NEW.sbom_digest IS DISTINCT FROM OLD.sbom_digest
        OR NEW.sbom_media_type IS DISTINCT FROM OLD.sbom_media_type)
       AND OLD.state <> 'QUARANTINED' THEN
        RAISE EXCEPTION 'SBOM can only attach while artifact is quarantined' USING ERRCODE = '55000';
    END IF;
    IF NEW.sbom_digest <> '' AND NEW.sbom_media_type = '' THEN
        RAISE EXCEPTION 'SBOM media type is required' USING ERRCODE = '23514';
    END IF;

    IF OLD.provenance_digest <> '' AND (
       NEW.provenance_digest IS DISTINCT FROM OLD.provenance_digest
       OR NEW.provenance_media_type IS DISTINCT FROM OLD.provenance_media_type) THEN
        RAISE EXCEPTION 'artifact provenance reference is immutable' USING ERRCODE = '55000';
    END IF;
    IF (NEW.provenance_digest IS DISTINCT FROM OLD.provenance_digest
        OR NEW.provenance_media_type IS DISTINCT FROM OLD.provenance_media_type)
       AND OLD.state <> 'SIGNED' THEN
        RAISE EXCEPTION 'provenance can only attach after signature' USING ERRCODE = '55000';
    END IF;
    IF NEW.provenance_digest <> '' AND NEW.provenance_media_type = '' THEN
        RAISE EXCEPTION 'provenance media type is required' USING ERRCODE = '23514';
    END IF;

    IF NEW.state IS DISTINCT FROM OLD.state THEN
        IF NOT (
            (OLD.state = 'DISCOVERED' AND NEW.state = 'QUARANTINED') OR
            (OLD.state = 'QUARANTINED' AND NEW.state IN ('SCANNED','REJECTED')) OR
            (OLD.state = 'SCANNED' AND NEW.state IN ('SIGNED','RELEASABLE')) OR
            (OLD.state = 'SIGNED' AND NEW.state = 'RELEASABLE')
        ) THEN
            RAISE EXCEPTION 'invalid artifact state transition: % -> %', OLD.state, NEW.state USING ERRCODE = '55000';
        END IF;
    END IF;

    IF NEW.state = 'RELEASABLE' THEN
        IF NEW.sbom_digest = '' OR NEW.sbom_media_type = ''
           OR NEW.provenance_digest = '' OR NEW.provenance_media_type = ''
           OR NOT EXISTS (
            SELECT 1 FROM build.scan_results s
            WHERE s.artifact_id = NEW.id
              AND s.passed = true
              AND btrim(s.scanner) <> ''
              AND btrim(s.policy_version) <> ''
        ) OR NOT EXISTS (
            SELECT 1 FROM build.signature_records s
            WHERE s.artifact_id = NEW.id
              AND s.digest = NEW.digest
              AND btrim(s.issuer) <> ''
              AND btrim(s.algorithm) <> ''
              AND btrim(s.signature) <> ''
              AND s.attachment_digest ~ '^sha256:[0-9a-f]{64}$'
        ) THEN
            RAISE EXCEPTION 'releasable artifact trust chain is incomplete' USING ERRCODE = '23514';
        END IF;
    END IF;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS build_artifact_state_and_trust ON build.artifacts;
CREATE TRIGGER build_artifact_state_and_trust
BEFORE UPDATE ON build.artifacts
FOR EACH ROW EXECUTE FUNCTION build.enforce_artifact_state_and_trust();
