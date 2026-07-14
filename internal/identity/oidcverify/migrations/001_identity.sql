CREATE SCHEMA IF NOT EXISTS platform_identity;

CREATE TABLE IF NOT EXISTS platform_identity.oidc_subjects (
  issuer text NOT NULL,
  subject text NOT NULL,
  user_id text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (issuer, subject),
  UNIQUE (issuer, user_id)
);

CREATE TABLE IF NOT EXISTS platform_identity.tenant_memberships (
  tenant_id text NOT NULL,
  user_id text NOT NULL,
  role text NOT NULL CHECK (role IN ('OWNER','OPERATOR','MEMBER')),
  state text NOT NULL CHECK (state IN ('ACTIVE','SUSPENDED','REVOKED')),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, user_id)
);
CREATE INDEX IF NOT EXISTS identity_active_membership_user_idx
  ON platform_identity.tenant_memberships(user_id) WHERE state = 'ACTIVE';
