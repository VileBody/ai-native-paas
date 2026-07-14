ALTER TABLE agent.tasks
  ADD COLUMN IF NOT EXISTS project_id text NOT NULL DEFAULT '';

UPDATE agent.tasks
SET project_id = COALESCE(payload->>'project_id', '')
WHERE project_id IS DISTINCT FROM COALESCE(payload->>'project_id', '');

CREATE INDEX IF NOT EXISTS agent_tasks_project_idx
  ON agent.tasks(tenant_id, project_id, agent_id)
  WHERE project_id <> '';

CREATE OR REPLACE FUNCTION agent.protect_task_identity() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF OLD.id <> NEW.id OR OLD.tenant_id <> NEW.tenant_id OR OLD.project_id <> NEW.project_id OR OLD.agent_id <> NEW.agent_id OR OLD.created_at <> NEW.created_at THEN
    RAISE EXCEPTION 'agent task identity is immutable' USING ERRCODE='55000';
  END IF;
  RETURN NEW;
END $$;
DROP TRIGGER IF EXISTS agent_task_identity_immutable ON agent.tasks;
CREATE TRIGGER agent_task_identity_immutable BEFORE UPDATE ON agent.tasks FOR EACH ROW EXECUTE FUNCTION agent.protect_task_identity();

CREATE OR REPLACE FUNCTION agent.check_task_payload() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.payload->>'id' <> NEW.id OR NEW.payload->>'tenant_id' <> NEW.tenant_id OR COALESCE(NEW.payload->>'project_id','') <> NEW.project_id OR NEW.payload->>'agent_id' <> NEW.agent_id OR NEW.payload->>'state' <> NEW.state OR (NEW.payload->>'version')::bigint <> NEW.version THEN RAISE EXCEPTION 'task payload drift' USING ERRCODE='23514'; END IF; RETURN NEW;
END $$;
