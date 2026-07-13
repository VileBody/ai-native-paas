CREATE OR REPLACE FUNCTION commerce.reject_append_only_mutation() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION '% is append-only', TG_TABLE_NAME USING ERRCODE = '55000';
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS commerce_usage_append_only ON commerce.usage_events;
CREATE TRIGGER commerce_usage_append_only BEFORE UPDATE OR DELETE ON commerce.usage_events FOR EACH ROW EXECUTE FUNCTION commerce.reject_append_only_mutation();
DROP TRIGGER IF EXISTS commerce_idempotency_append_only ON commerce.idempotency;
CREATE TRIGGER commerce_idempotency_append_only BEFORE UPDATE OR DELETE ON commerce.idempotency FOR EACH ROW EXECUTE FUNCTION commerce.reject_append_only_mutation();
DROP TRIGGER IF EXISTS commerce_audit_append_only ON commerce.audit;
CREATE TRIGGER commerce_audit_append_only BEFORE UPDATE OR DELETE ON commerce.audit FOR EACH ROW EXECUTE FUNCTION commerce.reject_append_only_mutation();
DROP TRIGGER IF EXISTS commerce_alerts_append_only ON commerce.reconciliation_alerts;
CREATE TRIGGER commerce_alerts_append_only BEFORE UPDATE OR DELETE ON commerce.reconciliation_alerts FOR EACH ROW EXECUTE FUNCTION commerce.reject_append_only_mutation();

CREATE OR REPLACE FUNCTION commerce.guard_plan_definition() RETURNS trigger AS $$
BEGIN
    IF NEW.id <> OLD.id OR NEW.created_at <> OLD.created_at THEN
        RAISE EXCEPTION 'plan definition identity is immutable' USING ERRCODE = '55000';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
DROP TRIGGER IF EXISTS commerce_plan_definition_guard ON commerce.plan_definitions;
CREATE TRIGGER commerce_plan_definition_guard BEFORE UPDATE ON commerce.plan_definitions FOR EACH ROW EXECUTE FUNCTION commerce.guard_plan_definition();

CREATE OR REPLACE FUNCTION commerce.guard_plan_version() RETURNS trigger AS $$
BEGIN
    IF NEW.id <> OLD.id OR NEW.definition_id <> OLD.definition_id OR NEW.policy_version <> OLD.policy_version OR
       NEW.number <> OLD.number OR NEW.effective_from <> OLD.effective_from OR NEW.created_at <> OLD.created_at THEN
        RAISE EXCEPTION 'plan version identity is immutable' USING ERRCODE = '55000';
    END IF;
    IF OLD.state <> 'DRAFT' AND (NEW.spec_hash <> OLD.spec_hash OR NEW.currency <> OLD.currency) THEN
        RAISE EXCEPTION 'active plan version is immutable' USING ERRCODE = '55000';
    END IF;
    IF OLD.state = 'DISABLED' AND NEW.state <> 'DISABLED' THEN
        RAISE EXCEPTION 'disabled plan version cannot be reactivated' USING ERRCODE = '55000';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
DROP TRIGGER IF EXISTS commerce_plan_version_guard ON commerce.plan_versions;
CREATE TRIGGER commerce_plan_version_guard BEFORE UPDATE ON commerce.plan_versions FOR EACH ROW EXECUTE FUNCTION commerce.guard_plan_version();

CREATE OR REPLACE FUNCTION commerce.guard_subscription() RETURNS trigger AS $$
BEGIN
    IF NEW.id <> OLD.id OR NEW.tenant_id <> OLD.tenant_id OR NEW.plan_version_id <> OLD.plan_version_id OR NEW.created_at <> OLD.created_at THEN
        RAISE EXCEPTION 'subscription identity is immutable' USING ERRCODE = '55000';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
DROP TRIGGER IF EXISTS commerce_subscription_guard ON commerce.subscriptions;
CREATE TRIGGER commerce_subscription_guard BEFORE UPDATE ON commerce.subscriptions FOR EACH ROW EXECUTE FUNCTION commerce.guard_subscription();

CREATE OR REPLACE FUNCTION commerce.guard_billing_period() RETURNS trigger AS $$
BEGIN
    IF NEW.id <> OLD.id OR NEW.tenant_id <> OLD.tenant_id OR NEW.subscription_id <> OLD.subscription_id OR
       NEW.plan_version_id <> OLD.plan_version_id OR NEW.start_at <> OLD.start_at OR NEW.end_at <> OLD.end_at OR NEW.created_at <> OLD.created_at THEN
        RAISE EXCEPTION 'billing period identity and price version are immutable' USING ERRCODE = '55000';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
DROP TRIGGER IF EXISTS commerce_billing_period_guard ON commerce.billing_periods;
CREATE TRIGGER commerce_billing_period_guard BEFORE UPDATE ON commerce.billing_periods FOR EACH ROW EXECUTE FUNCTION commerce.guard_billing_period();

CREATE OR REPLACE FUNCTION commerce.guard_account() RETURNS trigger AS $$
BEGIN
    IF NEW.tenant_id <> OLD.tenant_id OR NEW.created_at <> OLD.created_at THEN
        RAISE EXCEPTION 'commercial account identity is immutable' USING ERRCODE = '55000';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
DROP TRIGGER IF EXISTS commerce_account_guard ON commerce.commercial_accounts;
CREATE TRIGGER commerce_account_guard BEFORE UPDATE ON commerce.commercial_accounts FOR EACH ROW EXECUTE FUNCTION commerce.guard_account();

CREATE OR REPLACE FUNCTION commerce.guard_quota() RETURNS trigger AS $$
BEGIN
    IF NEW.id <> OLD.id OR NEW.tenant_id <> OLD.tenant_id OR NEW.resource <> OLD.resource OR NEW.policy_version <> OLD.policy_version OR
       NEW.quantity <> OLD.quantity OR NEW.idempotency_key <> OLD.idempotency_key OR NEW.fingerprint <> OLD.fingerprint OR
       NEW.expires_at <> OLD.expires_at OR NEW.created_at <> OLD.created_at THEN
        RAISE EXCEPTION 'quota reservation request is immutable' USING ERRCODE = '55000';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
DROP TRIGGER IF EXISTS commerce_quota_guard ON commerce.quota_reservations;
CREATE TRIGGER commerce_quota_guard BEFORE UPDATE ON commerce.quota_reservations FOR EACH ROW EXECUTE FUNCTION commerce.guard_quota();
