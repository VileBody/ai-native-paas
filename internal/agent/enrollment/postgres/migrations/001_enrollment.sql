CREATE SCHEMA IF NOT EXISTS agent_enrollment;

CREATE TABLE IF NOT EXISTS agent_enrollment.enrollments (
  token_hash text PRIMARY KEY,
  enrollment_id text NOT NULL UNIQUE,
  tenant_id text NOT NULL,
  project_id text NOT NULL,
  user_id text NOT NULL,
  agent_id text NOT NULL,
  scopes jsonb NOT NULL CHECK (jsonb_typeof(scopes) = 'array' AND jsonb_array_length(scopes) > 0),
  expires_at timestamptz NOT NULL,
  consumed_at timestamptz,
  CHECK (length(token_hash) = 64),
  CHECK (consumed_at IS NULL OR consumed_at <= expires_at)
);
CREATE INDEX IF NOT EXISTS agent_enrollments_expiry_idx
  ON agent_enrollment.enrollments(expires_at) WHERE consumed_at IS NULL;

CREATE TABLE IF NOT EXISTS agent_enrollment.refresh_credentials (
  token_hash text PRIMARY KEY,
  credential_id text NOT NULL UNIQUE,
  tenant_id text NOT NULL,
  project_id text NOT NULL,
  user_id text NOT NULL,
  agent_id text NOT NULL,
  scopes jsonb NOT NULL CHECK (jsonb_typeof(scopes) = 'array' AND jsonb_array_length(scopes) > 0),
  public_key text NOT NULL CHECK (length(public_key) BETWEEN 1 AND 16384),
  expires_at timestamptz NOT NULL,
  revoked_at timestamptz,
  CHECK (length(token_hash) = 64)
);
CREATE INDEX IF NOT EXISTS agent_refresh_binding_idx
  ON agent_enrollment.refresh_credentials(tenant_id, project_id, agent_id);
CREATE INDEX IF NOT EXISTS agent_refresh_expiry_idx
  ON agent_enrollment.refresh_credentials(expires_at) WHERE revoked_at IS NULL;
