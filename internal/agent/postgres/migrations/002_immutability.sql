CREATE OR REPLACE FUNCTION agent.reject_append_only_mutation() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  RAISE EXCEPTION 'agent record is append-only' USING ERRCODE='55000';
END $$;

DROP TRIGGER IF EXISTS agent_outbox_append_only ON agent.outbox;
CREATE TRIGGER agent_outbox_append_only BEFORE UPDATE OR DELETE ON agent.outbox FOR EACH ROW EXECUTE FUNCTION agent.reject_append_only_mutation();
DROP TRIGGER IF EXISTS agent_audit_append_only ON agent.audit;
CREATE TRIGGER agent_audit_append_only BEFORE UPDATE OR DELETE ON agent.audit FOR EACH ROW EXECUTE FUNCTION agent.reject_append_only_mutation();

CREATE OR REPLACE FUNCTION agent.protect_invocation_identity() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF OLD.id <> NEW.id OR OLD.tenant_id <> NEW.tenant_id OR OLD.agent_id <> NEW.agent_id OR OLD.task_id <> NEW.task_id OR OLD.tool <> NEW.tool OR OLD.idempotency_key <> NEW.idempotency_key OR OLD.fingerprint <> NEW.fingerprint OR OLD.created_at <> NEW.created_at THEN
    RAISE EXCEPTION 'agent invocation identity is immutable' USING ERRCODE='55000';
  END IF;
  RETURN NEW;
END $$;
DROP TRIGGER IF EXISTS agent_invocation_identity_immutable ON agent.invocations;
CREATE TRIGGER agent_invocation_identity_immutable BEFORE UPDATE ON agent.invocations FOR EACH ROW EXECUTE FUNCTION agent.protect_invocation_identity();

CREATE OR REPLACE FUNCTION agent.protect_approval_request_identity() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF OLD.id <> NEW.id OR OLD.tenant_id <> NEW.tenant_id OR OLD.agent_id <> NEW.agent_id OR OLD.task_id <> NEW.task_id OR OLD.action <> NEW.action OR OLD.resource_type <> NEW.resource_type OR OLD.resource_id <> NEW.resource_id OR OLD.payload_hash <> NEW.payload_hash OR OLD.expires_at <> NEW.expires_at OR OLD.created_at <> NEW.created_at THEN
    RAISE EXCEPTION 'approval request identity is immutable' USING ERRCODE='55000';
  END IF;
  RETURN NEW;
END $$;
DROP TRIGGER IF EXISTS agent_approval_request_identity_immutable ON agent.approval_requests;
CREATE TRIGGER agent_approval_request_identity_immutable BEFORE UPDATE ON agent.approval_requests FOR EACH ROW EXECUTE FUNCTION agent.protect_approval_request_identity();

CREATE OR REPLACE FUNCTION agent.protect_approval_grant_identity() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF OLD.id <> NEW.id OR OLD.request_id <> NEW.request_id OR OLD.tenant_id <> NEW.tenant_id OR OLD.agent_id <> NEW.agent_id OR OLD.task_id <> NEW.task_id OR OLD.approver_user_id <> NEW.approver_user_id OR OLD.action <> NEW.action OR OLD.resource_type <> NEW.resource_type OR OLD.resource_id <> NEW.resource_id OR OLD.payload_hash <> NEW.payload_hash OR OLD.expires_at <> NEW.expires_at OR OLD.created_at <> NEW.created_at THEN
    RAISE EXCEPTION 'approval grant identity is immutable' USING ERRCODE='55000';
  END IF;
  IF OLD.consumed_at IS NOT NULL AND NEW.consumed_at IS DISTINCT FROM OLD.consumed_at THEN
    RAISE EXCEPTION 'approval grant consumption is immutable' USING ERRCODE='55000';
  END IF;
  RETURN NEW;
END $$;
DROP TRIGGER IF EXISTS agent_approval_grant_identity_immutable ON agent.approval_grants;
CREATE TRIGGER agent_approval_grant_identity_immutable BEFORE UPDATE ON agent.approval_grants FOR EACH ROW EXECUTE FUNCTION agent.protect_approval_grant_identity();
