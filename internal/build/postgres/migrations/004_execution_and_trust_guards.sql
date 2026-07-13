ALTER TABLE build.builds
    ADD COLUMN IF NOT EXISTS runtime text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS backend text NOT NULL DEFAULT '' CHECK (backend IN ('','buildpacks','dockerfile-vm')),
    ADD COLUMN IF NOT EXISTS buildpack_id text NOT NULL DEFAULT '';

ALTER TABLE build.signature_records
    ADD COLUMN IF NOT EXISTS attachment_digest text NOT NULL DEFAULT ''
    CHECK (attachment_digest = '' OR attachment_digest ~ '^sha256:[0-9a-f]{64}$');


CREATE OR REPLACE FUNCTION build.enforce_build_identity_and_state() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.tenant_id IS DISTINCT FROM OLD.tenant_id
       OR NEW.identity IS DISTINCT FROM OLD.identity
       OR NEW.attempt IS DISTINCT FROM OLD.attempt
       OR NEW.source_revision IS DISTINCT FROM OLD.source_revision
       OR NEW.config IS DISTINCT FROM OLD.config
       OR NEW.builder_digest IS DISTINCT FROM OLD.builder_digest
       OR NEW.run_image_digest IS DISTINCT FROM OLD.run_image_digest
       OR NEW.platform_build_version IS DISTINCT FROM OLD.platform_build_version
       OR NEW.correlation_id IS DISTINCT FROM OLD.correlation_id
       OR NEW.original_correlation_id IS DISTINCT FROM OLD.original_correlation_id
       OR NEW.created_at IS DISTINCT FROM OLD.created_at THEN
        RAISE EXCEPTION 'build identity is immutable' USING ERRCODE = '55000';
    END IF;

    IF NEW.version <> OLD.version + 1 THEN
        RAISE EXCEPTION 'build version must increment exactly once' USING ERRCODE = '55000';
    END IF;

    IF (OLD.runtime <> '' AND NEW.runtime IS DISTINCT FROM OLD.runtime)
       OR (OLD.backend <> '' AND NEW.backend IS DISTINCT FROM OLD.backend)
       OR (OLD.buildpack_id <> '' AND NEW.buildpack_id IS DISTINCT FROM OLD.buildpack_id) THEN
        RAISE EXCEPTION 'build execution selection is immutable' USING ERRCODE = '55000';
    END IF;

    IF NEW.state IS DISTINCT FROM OLD.state AND NOT (
        (OLD.state = 'QUEUED' AND NEW.state IN ('FETCHING_SOURCE','CANCELED','SUPERSEDED')) OR
        (OLD.state = 'FETCHING_SOURCE' AND NEW.state IN ('DETECTING','FAILED_USER_CODE','FAILED_PLATFORM','CANCELED','SUPERSEDED','TIMED_OUT')) OR
        (OLD.state = 'DETECTING' AND NEW.state IN ('BUILDING','FAILED_USER_CODE','FAILED_PLATFORM','CANCELED','SUPERSEDED','TIMED_OUT')) OR
        (OLD.state = 'BUILDING' AND NEW.state IN ('EXPORTING','FAILED_USER_CODE','FAILED_PLATFORM','CANCELED','SUPERSEDED','TIMED_OUT')) OR
        (OLD.state = 'EXPORTING' AND NEW.state IN ('SCANNING','FAILED_USER_CODE','FAILED_PLATFORM','CANCELED','SUPERSEDED','TIMED_OUT')) OR
        (OLD.state = 'SCANNING' AND NEW.state IN ('SIGNING','FAILED_USER_CODE','FAILED_PLATFORM','CANCELED','TIMED_OUT')) OR
        (OLD.state = 'SIGNING' AND NEW.state IN ('SUCCEEDED','FAILED_USER_CODE','FAILED_PLATFORM','CANCELED','TIMED_OUT'))
    ) THEN
        RAISE EXCEPTION 'invalid build state transition: % -> %', OLD.state, NEW.state USING ERRCODE = '55000';
    END IF;

    IF NEW.state IN ('BUILDING','EXPORTING','SCANNING','SIGNING','SUCCEEDED')
       AND (NEW.runtime = '' OR NEW.backend = '') THEN
        RAISE EXCEPTION 'running build requires persisted execution selection' USING ERRCODE = '23514';
    END IF;
    IF NEW.backend = 'buildpacks' AND NEW.state IN ('BUILDING','EXPORTING','SCANNING','SIGNING','SUCCEEDED') AND NEW.buildpack_id = '' THEN
        RAISE EXCEPTION 'buildpacks backend requires buildpack id' USING ERRCODE = '23514';
    END IF;
    IF NEW.state = 'SUCCEEDED' AND NEW.artifact_id IS NULL THEN
        RAISE EXCEPTION 'successful build requires artifact id' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS build_identity_and_state_guard ON build.builds;
CREATE TRIGGER build_identity_and_state_guard
BEFORE UPDATE ON build.builds
FOR EACH ROW EXECUTE FUNCTION build.enforce_build_identity_and_state();

CREATE OR REPLACE FUNCTION build.reject_trust_record_mutation() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF NEW IS DISTINCT FROM OLD THEN
        RAISE EXCEPTION 'artifact trust records are immutable' USING ERRCODE = '55000';
    END IF;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS build_scan_result_immutable ON build.scan_results;
CREATE TRIGGER build_scan_result_immutable
BEFORE UPDATE OR DELETE ON build.scan_results
FOR EACH ROW EXECUTE FUNCTION build.reject_trust_record_mutation();

DROP TRIGGER IF EXISTS build_signature_record_immutable ON build.signature_records;
CREATE TRIGGER build_signature_record_immutable
BEFORE UPDATE OR DELETE ON build.signature_records
FOR EACH ROW EXECUTE FUNCTION build.reject_trust_record_mutation();

CREATE OR REPLACE FUNCTION build.enforce_artifact_state_and_trust() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    -- A single repository transaction may intentionally collapse multiple
    -- validated domain transitions (for example SCANNED -> SIGNED -> RELEASABLE)
    -- while committing the scan, signature, and parent artifact atomically.
    -- Reject stale/regressing writes, but permit any strictly advancing version.
    IF NEW.version <= OLD.version THEN
        RAISE EXCEPTION 'artifact version must advance' USING ERRCODE = '55000';
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
        IF NEW.sbom_digest = '' OR NOT EXISTS (
            SELECT 1 FROM build.scan_results s
            WHERE s.artifact_id = NEW.id AND s.passed = true
        ) OR NOT EXISTS (
            SELECT 1 FROM build.signature_records s
            WHERE s.artifact_id = NEW.id
              AND s.digest = NEW.digest
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
BEFORE UPDATE OF state, sbom_digest ON build.artifacts
FOR EACH ROW EXECUTE FUNCTION build.enforce_artifact_state_and_trust();
