CREATE SCHEMA IF NOT EXISTS agent;

CREATE TABLE IF NOT EXISTS agent.principals (
  id text PRIMARY KEY,
  tenant_id text NOT NULL,
  on_behalf_of_user_id text NOT NULL,
  state text NOT NULL,
  version bigint NOT NULL CHECK (version >= 1),
  payload jsonb NOT NULL,
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL
);
CREATE INDEX IF NOT EXISTS agent_principals_tenant_idx ON agent.principals(tenant_id);

CREATE TABLE IF NOT EXISTS agent.tasks (
  id text PRIMARY KEY,
  tenant_id text NOT NULL,
  agent_id text NOT NULL,
  state text NOT NULL,
  version bigint NOT NULL CHECK (version >= 1),
  payload jsonb NOT NULL,
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL
);
CREATE INDEX IF NOT EXISTS agent_tasks_tenant_idx ON agent.tasks(tenant_id, agent_id);

CREATE TABLE IF NOT EXISTS agent.invocations (
  id text PRIMARY KEY,
  tenant_id text NOT NULL,
  agent_id text NOT NULL,
  task_id text NOT NULL,
  tool text NOT NULL,
  idempotency_key text NOT NULL,
  fingerprint text NOT NULL,
  state text NOT NULL,
  external_operation_id text NOT NULL DEFAULT '',
  version bigint NOT NULL CHECK (version >= 1),
  payload jsonb NOT NULL,
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  UNIQUE(tenant_id, task_id, idempotency_key)
);
CREATE INDEX IF NOT EXISTS agent_invocations_task_idx ON agent.invocations(tenant_id, task_id, created_at);

CREATE TABLE IF NOT EXISTS agent.approval_requests (
  id text PRIMARY KEY,
  tenant_id text NOT NULL,
  agent_id text NOT NULL,
  task_id text NOT NULL,
  action text NOT NULL,
  resource_type text NOT NULL,
  resource_id text NOT NULL,
  payload_hash text NOT NULL,
  state text NOT NULL,
  expires_at timestamptz NOT NULL,
  version bigint NOT NULL CHECK (version >= 1),
  payload jsonb NOT NULL,
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL
);
CREATE INDEX IF NOT EXISTS agent_approval_requests_idx ON agent.approval_requests(tenant_id, task_id, state);

CREATE TABLE IF NOT EXISTS agent.approval_grants (
  id text PRIMARY KEY,
  request_id text NOT NULL UNIQUE,
  tenant_id text NOT NULL,
  agent_id text NOT NULL,
  task_id text NOT NULL,
  approver_user_id text NOT NULL,
  action text NOT NULL,
  resource_type text NOT NULL,
  resource_id text NOT NULL,
  payload_hash text NOT NULL,
  expires_at timestamptz NOT NULL,
  consumed_at timestamptz,
  version bigint NOT NULL CHECK (version >= 1),
  payload jsonb NOT NULL,
  created_at timestamptz NOT NULL
);
CREATE INDEX IF NOT EXISTS agent_approval_grants_idx ON agent.approval_grants(tenant_id, task_id, consumed_at);

CREATE TABLE IF NOT EXISTS agent.outbox (
  id text PRIMARY KEY,
  tenant_id text NOT NULL,
  topic text NOT NULL,
  aggregate_id text NOT NULL,
  payload jsonb NOT NULL,
  created_at timestamptz NOT NULL
);
CREATE INDEX IF NOT EXISTS agent_outbox_created_idx ON agent.outbox(created_at);

CREATE TABLE IF NOT EXISTS agent.audit (
  id text PRIMARY KEY,
  tenant_id text NOT NULL,
  task_id text NOT NULL,
  agent_id text NOT NULL,
  tool text NOT NULL,
  outcome text NOT NULL,
  payload jsonb NOT NULL,
  created_at timestamptz NOT NULL
);
CREATE INDEX IF NOT EXISTS agent_audit_task_idx ON agent.audit(tenant_id, task_id, created_at);
