CREATE OR REPLACE FUNCTION agent_enrollment.protect_enrollment_identity() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF OLD.token_hash <> NEW.token_hash OR OLD.enrollment_id <> NEW.enrollment_id OR
     OLD.tenant_id <> NEW.tenant_id OR OLD.project_id <> NEW.project_id OR
     OLD.user_id <> NEW.user_id OR OLD.agent_id <> NEW.agent_id OR
     OLD.scopes <> NEW.scopes OR OLD.expires_at <> NEW.expires_at THEN
    RAISE EXCEPTION 'enrollment identity is immutable' USING ERRCODE='55000';
  END IF;
  IF OLD.consumed_at IS NOT NULL AND NEW.consumed_at IS DISTINCT FROM OLD.consumed_at THEN
    RAISE EXCEPTION 'enrollment consumption is immutable' USING ERRCODE='55000';
  END IF;
  RETURN NEW;
END $$;
DROP TRIGGER IF EXISTS agent_enrollment_identity_immutable ON agent_enrollment.enrollments;
CREATE TRIGGER agent_enrollment_identity_immutable
  BEFORE UPDATE ON agent_enrollment.enrollments
  FOR EACH ROW EXECUTE FUNCTION agent_enrollment.protect_enrollment_identity();

CREATE OR REPLACE FUNCTION agent_enrollment.protect_refresh_identity() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF OLD.token_hash <> NEW.token_hash OR OLD.credential_id <> NEW.credential_id OR
     OLD.tenant_id <> NEW.tenant_id OR OLD.project_id <> NEW.project_id OR
     OLD.user_id <> NEW.user_id OR OLD.agent_id <> NEW.agent_id OR
     OLD.scopes <> NEW.scopes OR OLD.public_key <> NEW.public_key OR
     OLD.expires_at <> NEW.expires_at THEN
    RAISE EXCEPTION 'refresh credential identity is immutable' USING ERRCODE='55000';
  END IF;
  IF OLD.revoked_at IS NOT NULL AND NEW.revoked_at IS DISTINCT FROM OLD.revoked_at THEN
    RAISE EXCEPTION 'refresh credential revocation is immutable' USING ERRCODE='55000';
  END IF;
  RETURN NEW;
END $$;
DROP TRIGGER IF EXISTS agent_refresh_identity_immutable ON agent_enrollment.refresh_credentials;
CREATE TRIGGER agent_refresh_identity_immutable
  BEFORE UPDATE ON agent_enrollment.refresh_credentials
  FOR EACH ROW EXECUTE FUNCTION agent_enrollment.protect_refresh_identity();
