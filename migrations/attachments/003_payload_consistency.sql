CREATE OR REPLACE FUNCTION attachments.check_secret_set_payload()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.payload->>'id' IS DISTINCT FROM NEW.id
       OR NEW.payload->>'tenant_id' IS DISTINCT FROM NEW.tenant_id
       OR NEW.payload->>'application_id' IS DISTINCT FROM NEW.application_id
       OR NEW.payload->>'environment_id' IS DISTINCT FROM NEW.environment_id
       OR NEW.payload->>'provider_path' IS DISTINCT FROM NEW.provider_path
       OR NEW.payload->>'version' IS DISTINCT FROM NEW.version::text THEN
        RAISE EXCEPTION 'attachments secret_sets payload drift' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER attachments_secret_set_payload_consistency
    BEFORE INSERT OR UPDATE ON attachments.secret_sets
    FOR EACH ROW EXECUTE FUNCTION attachments.check_secret_set_payload();

CREATE OR REPLACE FUNCTION attachments.check_secret_payload()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.payload->>'id' IS DISTINCT FROM NEW.id
       OR NEW.payload->>'tenant_id' IS DISTINCT FROM NEW.tenant_id
       OR NEW.payload->>'secret_set_id' IS DISTINCT FROM NEW.secret_set_id
       OR NEW.payload->>'name' IS DISTINCT FROM NEW.name
       OR NEW.payload->>'scope' IS DISTINCT FROM NEW.scope
       OR NEW.payload->>'phase' IS DISTINCT FROM NEW.phase
       OR NEW.payload->>'provider_ref' IS DISTINCT FROM NEW.provider_ref
       OR NEW.payload->>'provider_version' IS DISTINCT FROM NEW.provider_version
       OR NEW.payload->>'version' IS DISTINCT FROM NEW.version::text
       OR COALESCE((NEW.payload->>'deleted')::boolean, false) IS DISTINCT FROM NEW.deleted THEN
        RAISE EXCEPTION 'attachments secrets payload drift' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER attachments_secret_payload_consistency
    BEFORE INSERT OR UPDATE ON attachments.secrets
    FOR EACH ROW EXECUTE FUNCTION attachments.check_secret_payload();

CREATE OR REPLACE FUNCTION attachments.check_plan_payload()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.payload->>'id' IS DISTINCT FROM NEW.id
       OR NEW.payload->>'version' IS DISTINCT FROM NEW.version::text
       OR NEW.payload->>'type' IS DISTINCT FROM NEW.service_type
       OR NEW.payload->>'provider' IS DISTINCT FROM NEW.provider
       OR NEW.payload->>'provider_plan' IS DISTINCT FROM NEW.provider_plan
       OR NEW.payload->>'provider_mapping_version' IS DISTINCT FROM NEW.provider_mapping_version
       OR (NEW.payload->>'enabled')::boolean IS DISTINCT FROM NEW.enabled THEN
        RAISE EXCEPTION 'attachments service_plans payload drift' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER attachments_plan_payload_consistency
    BEFORE INSERT ON attachments.service_plans
    FOR EACH ROW EXECUTE FUNCTION attachments.check_plan_payload();

CREATE OR REPLACE FUNCTION attachments.check_instance_payload()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.payload->>'id' IS DISTINCT FROM NEW.id
       OR NEW.payload->>'tenant_id' IS DISTINCT FROM NEW.tenant_id
       OR COALESCE(NEW.payload->>'application_id', '') IS DISTINCT FROM NEW.application_id
       OR COALESCE(NEW.payload->>'environment_id', '') IS DISTINCT FROM NEW.environment_id
       OR NEW.payload->>'name' IS DISTINCT FROM NEW.name
       OR NEW.payload->>'plan_id' IS DISTINCT FROM NEW.plan_id
       OR NEW.payload->>'plan_version' IS DISTINCT FROM NEW.plan_version::text
       OR NEW.payload->>'type' IS DISTINCT FROM NEW.service_type
       OR NEW.payload->>'state' IS DISTINCT FROM NEW.state
       OR NEW.payload->>'provider_operation_key' IS DISTINCT FROM NEW.provider_operation_key
       OR COALESCE(NEW.payload->>'provider_id', '') IS DISTINCT FROM NEW.provider_id
       OR COALESCE(NEW.payload->>'provider_endpoint', '') IS DISTINCT FROM NEW.provider_endpoint
       OR NEW.payload->>'version' IS DISTINCT FROM NEW.version::text THEN
        RAISE EXCEPTION 'attachments service_instances payload drift' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER attachments_instance_payload_consistency
    BEFORE INSERT OR UPDATE ON attachments.service_instances
    FOR EACH ROW EXECUTE FUNCTION attachments.check_instance_payload();

CREATE OR REPLACE FUNCTION attachments.check_binding_payload()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.payload->>'id' IS DISTINCT FROM NEW.id
       OR NEW.payload->>'tenant_id' IS DISTINCT FROM NEW.tenant_id
       OR NEW.payload->>'application_id' IS DISTINCT FROM NEW.application_id
       OR NEW.payload->>'environment_id' IS DISTINCT FROM NEW.environment_id
       OR NEW.payload->>'instance_id' IS DISTINCT FROM NEW.instance_id
       OR NEW.payload->>'state' IS DISTINCT FROM NEW.state
       OR COALESCE(NEW.payload->>'provider_credential_id', '') IS DISTINCT FROM NEW.provider_credential_id
       OR NEW.payload->>'version' IS DISTINCT FROM NEW.version::text THEN
        RAISE EXCEPTION 'attachments service_bindings payload drift' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER attachments_binding_payload_consistency
    BEFORE INSERT OR UPDATE ON attachments.service_bindings
    FOR EACH ROW EXECUTE FUNCTION attachments.check_binding_payload();

CREATE OR REPLACE FUNCTION attachments.check_claim_payload()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.payload->>'id' IS DISTINCT FROM NEW.id
       OR NEW.payload->>'tenant_id' IS DISTINCT FROM NEW.tenant_id
       OR NEW.payload->>'application_id' IS DISTINCT FROM NEW.application_id
       OR NEW.payload->>'environment_id' IS DISTINCT FROM NEW.environment_id
       OR NEW.payload->>'hostname' IS DISTINCT FROM NEW.hostname
       OR (NEW.payload->>'generated')::boolean IS DISTINCT FROM NEW.generated
       OR NEW.payload->>'state' IS DISTINCT FROM NEW.state
       OR COALESCE(NEW.payload->>'challenge_name', '') IS DISTINCT FROM NEW.challenge_name
       OR COALESCE(NEW.payload->>'challenge_value', '') IS DISTINCT FROM NEW.challenge_value
       OR NEW.payload->>'version' IS DISTINCT FROM NEW.version::text THEN
        RAISE EXCEPTION 'attachments domain_claims payload drift' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER attachments_claim_payload_consistency
    BEFORE INSERT OR UPDATE ON attachments.domain_claims
    FOR EACH ROW EXECUTE FUNCTION attachments.check_claim_payload();

CREATE OR REPLACE FUNCTION attachments.check_snapshot_payload()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.payload->'value'->>'snapshot_id' IS DISTINCT FROM NEW.id
       OR NEW.payload->'value'->>'tenant_id' IS DISTINCT FROM NEW.tenant_id
       OR NEW.payload->'value'->>'application_id' IS DISTINCT FROM NEW.application_id
       OR NEW.payload->'value'->>'environment_id' IS DISTINCT FROM NEW.environment_id
       OR NEW.payload->'value'->>'version' IS DISTINCT FROM NEW.version::text
       OR NEW.payload->>'content_hash' IS DISTINCT FROM NEW.content_hash
       OR NEW.payload->'value'->>'secret_set_ref' IS DISTINCT FROM NEW.secret_set_ref THEN
        RAISE EXCEPTION 'attachments snapshots payload drift' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER attachments_snapshot_payload_consistency
    BEFORE INSERT ON attachments.snapshots
    FOR EACH ROW EXECUTE FUNCTION attachments.check_snapshot_payload();
