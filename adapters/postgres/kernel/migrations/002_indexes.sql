CREATE INDEX operations_tenant_created_idx
    ON kernel.operations (tenant_id, created_at DESC);

CREATE INDEX outbox_dispatch_idx
    ON kernel.outbox_events (state, lease_until, created_at)
    WHERE state <> 'PUBLISHED';

CREATE INDEX audit_tenant_occurred_idx
    ON kernel.audit_records (tenant_id, occurred_at, audit_id);

CREATE INDEX memberships_principal_state_idx
    ON kernel.memberships (principal_id, state, organization_id);
