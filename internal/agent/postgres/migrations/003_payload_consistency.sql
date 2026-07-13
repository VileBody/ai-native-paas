CREATE OR REPLACE FUNCTION agent.check_principal_payload() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.payload->>'id' <> NEW.id OR NEW.payload->>'tenant_id' <> NEW.tenant_id OR NEW.payload->>'on_behalf_of_user_id' <> NEW.on_behalf_of_user_id OR NEW.payload->>'state' <> NEW.state OR (NEW.payload->>'version')::bigint <> NEW.version THEN RAISE EXCEPTION 'principal payload drift' USING ERRCODE='23514'; END IF; RETURN NEW;
END $$;
DROP TRIGGER IF EXISTS agent_principal_payload_consistency ON agent.principals;
CREATE TRIGGER agent_principal_payload_consistency BEFORE INSERT OR UPDATE ON agent.principals FOR EACH ROW EXECUTE FUNCTION agent.check_principal_payload();

CREATE OR REPLACE FUNCTION agent.check_task_payload() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.payload->>'id' <> NEW.id OR NEW.payload->>'tenant_id' <> NEW.tenant_id OR NEW.payload->>'agent_id' <> NEW.agent_id OR NEW.payload->>'state' <> NEW.state OR (NEW.payload->>'version')::bigint <> NEW.version THEN RAISE EXCEPTION 'task payload drift' USING ERRCODE='23514'; END IF; RETURN NEW;
END $$;
DROP TRIGGER IF EXISTS agent_task_payload_consistency ON agent.tasks;
CREATE TRIGGER agent_task_payload_consistency BEFORE INSERT OR UPDATE ON agent.tasks FOR EACH ROW EXECUTE FUNCTION agent.check_task_payload();

CREATE OR REPLACE FUNCTION agent.check_invocation_payload() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.payload->>'id' <> NEW.id OR NEW.payload->>'tenant_id' <> NEW.tenant_id OR NEW.payload->>'agent_id' <> NEW.agent_id OR NEW.payload->>'task_id' <> NEW.task_id OR NEW.payload->>'tool' <> NEW.tool OR NEW.payload->>'idempotency_key' <> NEW.idempotency_key OR NEW.payload->>'fingerprint' <> NEW.fingerprint OR NEW.payload->>'state' <> NEW.state OR COALESCE(NEW.payload->>'external_operation_id','') <> NEW.external_operation_id OR (NEW.payload->>'version')::bigint <> NEW.version THEN RAISE EXCEPTION 'invocation payload drift' USING ERRCODE='23514'; END IF; RETURN NEW;
END $$;
DROP TRIGGER IF EXISTS agent_invocation_payload_consistency ON agent.invocations;
CREATE TRIGGER agent_invocation_payload_consistency BEFORE INSERT OR UPDATE ON agent.invocations FOR EACH ROW EXECUTE FUNCTION agent.check_invocation_payload();

CREATE OR REPLACE FUNCTION agent.check_approval_request_payload() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.payload->>'id' <> NEW.id OR NEW.payload->>'tenant_id' <> NEW.tenant_id OR NEW.payload->>'agent_id' <> NEW.agent_id OR NEW.payload->>'task_id' <> NEW.task_id OR NEW.payload->>'action' <> NEW.action OR NEW.payload#>>'{resource,type}' <> NEW.resource_type OR NEW.payload#>>'{resource,id}' <> NEW.resource_id OR NEW.payload->>'payload_hash' <> NEW.payload_hash OR NEW.payload->>'state' <> NEW.state OR (NEW.payload->>'version')::bigint <> NEW.version THEN RAISE EXCEPTION 'approval request payload drift' USING ERRCODE='23514'; END IF; RETURN NEW;
END $$;
DROP TRIGGER IF EXISTS agent_approval_request_payload_consistency ON agent.approval_requests;
CREATE TRIGGER agent_approval_request_payload_consistency BEFORE INSERT OR UPDATE ON agent.approval_requests FOR EACH ROW EXECUTE FUNCTION agent.check_approval_request_payload();

CREATE OR REPLACE FUNCTION agent.check_approval_grant_payload() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.payload->>'id' <> NEW.id OR NEW.payload->>'request_id' <> NEW.request_id OR NEW.payload->>'tenant_id' <> NEW.tenant_id OR NEW.payload->>'agent_id' <> NEW.agent_id OR NEW.payload->>'task_id' <> NEW.task_id OR NEW.payload->>'action' <> NEW.action OR NEW.payload#>>'{resource,type}' <> NEW.resource_type OR NEW.payload#>>'{resource,id}' <> NEW.resource_id OR NEW.payload->>'payload_hash' <> NEW.payload_hash OR (NEW.payload->>'version')::bigint <> NEW.version THEN RAISE EXCEPTION 'approval grant payload drift' USING ERRCODE='23514'; END IF; RETURN NEW;
END $$;
DROP TRIGGER IF EXISTS agent_approval_grant_payload_consistency ON agent.approval_grants;
CREATE TRIGGER agent_approval_grant_payload_consistency BEFORE INSERT OR UPDATE ON agent.approval_grants FOR EACH ROW EXECUTE FUNCTION agent.check_approval_grant_payload();
