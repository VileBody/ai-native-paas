ALTER TABLE source.repositories
    ADD COLUMN IF NOT EXISTS bootstrap_revision text NOT NULL DEFAULT '';

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conname = 'source_repository_bootstrap_revision_sha'
          AND conrelid = 'source.repositories'::regclass
    ) THEN
        ALTER TABLE source.repositories
            ADD CONSTRAINT source_repository_bootstrap_revision_sha
            CHECK (
                bootstrap_revision = '' OR
                bootstrap_revision ~ '^[0-9a-f]{40}([0-9a-f]{24})?$'
            );
    END IF;
END;
$$;

CREATE OR REPLACE FUNCTION source.enforce_repository_bootstrap_revision() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF OLD.bootstrap_revision <> ''
       AND NEW.bootstrap_revision IS DISTINCT FROM OLD.bootstrap_revision THEN
        RAISE EXCEPTION 'repository bootstrap_revision is immutable once assigned' USING ERRCODE = '55000';
    END IF;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS source_repository_bootstrap_revision ON source.repositories;
CREATE TRIGGER source_repository_bootstrap_revision
BEFORE UPDATE OF bootstrap_revision ON source.repositories
FOR EACH ROW EXECUTE FUNCTION source.enforce_repository_bootstrap_revision();
