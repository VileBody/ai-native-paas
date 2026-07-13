-- PostgreSQL fires triggers with the same timing and event in name order.
-- Provider identity is the stronger invariant and must be reported before the
-- generic payload-consistency guard when a caller mutates provider_id directly.
DROP TRIGGER IF EXISTS attachments_service_provider_identity
    ON attachments.service_instances;

DROP TRIGGER IF EXISTS attachments_00_service_provider_identity
    ON attachments.service_instances;

CREATE TRIGGER attachments_00_service_provider_identity
    BEFORE UPDATE ON attachments.service_instances
    FOR EACH ROW EXECUTE FUNCTION attachments.guard_provider_identity();
