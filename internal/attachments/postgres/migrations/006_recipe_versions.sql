CREATE TABLE attachments.recipe_versions (
    recipe_id text NOT NULL,
    version text NOT NULL,
    content_digest text NOT NULL,
    signature_digest text NOT NULL,
    signing_key_id text NOT NULL,
    payload jsonb NOT NULL,
    activated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (recipe_id, version),
    UNIQUE (recipe_id, content_digest)
);

CREATE OR REPLACE FUNCTION attachments.check_recipe_version_payload()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.payload->>'recipe_id' IS DISTINCT FROM NEW.recipe_id
       OR NEW.payload->>'version' IS DISTINCT FROM NEW.version
       OR NEW.payload->>'content_digest' IS DISTINCT FROM NEW.content_digest
       OR NEW.payload->>'signature_digest' IS DISTINCT FROM NEW.signature_digest
       OR NEW.payload->>'signing_key_id' IS DISTINCT FROM NEW.signing_key_id THEN
        RAISE EXCEPTION 'attachments recipe_versions payload drift'
            USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER attachments_recipe_version_payload_consistency
    BEFORE INSERT ON attachments.recipe_versions
    FOR EACH ROW EXECUTE FUNCTION attachments.check_recipe_version_payload();

CREATE TRIGGER attachments_recipe_versions_append_only
    BEFORE UPDATE OR DELETE ON attachments.recipe_versions
    FOR EACH ROW EXECUTE FUNCTION attachments.reject_append_only_mutation();
