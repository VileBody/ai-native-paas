CREATE OR REPLACE FUNCTION commerce.guard_payload_consistency() RETURNS trigger AS $$
BEGIN
    IF TG_TABLE_NAME = 'plan_definitions' THEN
        IF NEW.payload->>'ID' IS DISTINCT FROM NEW.id OR NEW.payload->>'Name' IS DISTINCT FROM NEW.name OR
           (NEW.payload->>'Version')::bigint IS DISTINCT FROM NEW.version THEN
            RAISE EXCEPTION 'plan definition payload drift' USING ERRCODE = '23514';
        END IF;
    ELSIF TG_TABLE_NAME = 'plan_versions' THEN
        IF NEW.payload->>'ID' IS DISTINCT FROM NEW.id OR NEW.payload->>'DefinitionID' IS DISTINCT FROM NEW.definition_id OR
           NEW.payload->>'PolicyVersion' IS DISTINCT FROM NEW.policy_version OR (NEW.payload->>'Number')::bigint IS DISTINCT FROM NEW.number OR
           NEW.payload->>'State' IS DISTINCT FROM NEW.state OR (NEW.payload->>'Version')::bigint IS DISTINCT FROM NEW.version THEN
            RAISE EXCEPTION 'plan version payload drift' USING ERRCODE = '23514';
        END IF;
    ELSIF TG_TABLE_NAME = 'subscriptions' THEN
        IF NEW.payload->>'ID' IS DISTINCT FROM NEW.id OR NEW.payload->>'TenantID' IS DISTINCT FROM NEW.tenant_id OR
           NEW.payload->>'PlanVersionID' IS DISTINCT FROM NEW.plan_version_id OR NEW.payload->>'State' IS DISTINCT FROM NEW.state OR
           (NEW.payload->>'Version')::bigint IS DISTINCT FROM NEW.version THEN
            RAISE EXCEPTION 'subscription payload drift' USING ERRCODE = '23514';
        END IF;
    ELSIF TG_TABLE_NAME = 'billing_periods' THEN
        IF NEW.payload->>'ID' IS DISTINCT FROM NEW.id OR NEW.payload->>'TenantID' IS DISTINCT FROM NEW.tenant_id OR
           NEW.payload->>'SubscriptionID' IS DISTINCT FROM NEW.subscription_id OR NEW.payload->>'PlanVersionID' IS DISTINCT FROM NEW.plan_version_id OR
           NEW.payload->>'State' IS DISTINCT FROM NEW.state OR (NEW.payload->>'Version')::bigint IS DISTINCT FROM NEW.version THEN
            RAISE EXCEPTION 'billing period payload drift' USING ERRCODE = '23514';
        END IF;
    ELSIF TG_TABLE_NAME = 'commercial_accounts' THEN
        IF NEW.payload->>'TenantID' IS DISTINCT FROM NEW.tenant_id OR NEW.payload->>'State' IS DISTINCT FROM NEW.state OR
           (NEW.payload->>'Version')::bigint IS DISTINCT FROM NEW.version THEN
            RAISE EXCEPTION 'commercial account payload drift' USING ERRCODE = '23514';
        END IF;
    ELSIF TG_TABLE_NAME = 'quota_reservations' THEN
        IF NEW.payload->>'ID' IS DISTINCT FROM NEW.id OR NEW.payload->>'TenantID' IS DISTINCT FROM NEW.tenant_id OR
           NEW.payload->>'Resource' IS DISTINCT FROM NEW.resource OR NEW.payload->>'PolicyVersion' IS DISTINCT FROM NEW.policy_version OR
           (NEW.payload->>'Quantity')::bigint IS DISTINCT FROM NEW.quantity OR NEW.payload->>'State' IS DISTINCT FROM NEW.state OR
           NEW.payload->>'IdempotencyKey' IS DISTINCT FROM NEW.idempotency_key OR NEW.payload->>'Fingerprint' IS DISTINCT FROM NEW.fingerprint OR
           (NEW.payload->>'Version')::bigint IS DISTINCT FROM NEW.version THEN
            RAISE EXCEPTION 'quota reservation payload drift' USING ERRCODE = '23514';
        END IF;
    ELSIF TG_TABLE_NAME = 'usage_events' THEN
        IF NEW.payload->>'id' IS DISTINCT FROM NEW.id OR NEW.payload->>'tenant_id' IS DISTINCT FROM NEW.tenant_id OR
           NEW.payload->>'period_id' IS DISTINCT FROM NEW.period_id OR NEW.payload->>'resource_type' IS DISTINCT FROM NEW.resource_type OR
           NEW.payload->>'resource_id' IS DISTINCT FROM NEW.resource_id OR NEW.payload->>'meter' IS DISTINCT FROM NEW.meter OR
           NEW.payload->>'kind' IS DISTINCT FROM NEW.kind OR (NEW.payload->>'quantity')::bigint IS DISTINCT FROM NEW.quantity OR
           NEW.payload->>'idempotency_key' IS DISTINCT FROM NEW.idempotency_key OR (NEW.payload->>'version')::bigint IS DISTINCT FROM NEW.version THEN
            RAISE EXCEPTION 'usage event payload drift' USING ERRCODE = '23514';
        END IF;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS commerce_plan_definitions_payload_guard ON commerce.plan_definitions;
CREATE TRIGGER commerce_plan_definitions_payload_guard BEFORE INSERT OR UPDATE ON commerce.plan_definitions FOR EACH ROW EXECUTE FUNCTION commerce.guard_payload_consistency();
DROP TRIGGER IF EXISTS commerce_plan_versions_payload_guard ON commerce.plan_versions;
CREATE TRIGGER commerce_plan_versions_payload_guard BEFORE INSERT OR UPDATE ON commerce.plan_versions FOR EACH ROW EXECUTE FUNCTION commerce.guard_payload_consistency();
DROP TRIGGER IF EXISTS commerce_subscriptions_payload_guard ON commerce.subscriptions;
CREATE TRIGGER commerce_subscriptions_payload_guard BEFORE INSERT OR UPDATE ON commerce.subscriptions FOR EACH ROW EXECUTE FUNCTION commerce.guard_payload_consistency();
DROP TRIGGER IF EXISTS commerce_billing_periods_payload_guard ON commerce.billing_periods;
CREATE TRIGGER commerce_billing_periods_payload_guard BEFORE INSERT OR UPDATE ON commerce.billing_periods FOR EACH ROW EXECUTE FUNCTION commerce.guard_payload_consistency();
DROP TRIGGER IF EXISTS commerce_accounts_payload_guard ON commerce.commercial_accounts;
CREATE TRIGGER commerce_accounts_payload_guard BEFORE INSERT OR UPDATE ON commerce.commercial_accounts FOR EACH ROW EXECUTE FUNCTION commerce.guard_payload_consistency();
DROP TRIGGER IF EXISTS commerce_quota_payload_guard ON commerce.quota_reservations;
CREATE TRIGGER commerce_quota_payload_guard BEFORE INSERT OR UPDATE ON commerce.quota_reservations FOR EACH ROW EXECUTE FUNCTION commerce.guard_payload_consistency();
DROP TRIGGER IF EXISTS commerce_usage_payload_guard ON commerce.usage_events;
CREATE TRIGGER commerce_usage_payload_guard BEFORE INSERT ON commerce.usage_events FOR EACH ROW EXECUTE FUNCTION commerce.guard_payload_consistency();
