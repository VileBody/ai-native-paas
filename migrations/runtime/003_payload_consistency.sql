CREATE OR REPLACE FUNCTION runtime.guard_payload_consistency() RETURNS trigger AS $$
BEGIN
    IF TG_TABLE_NAME = 'applications' THEN
        IF NEW.payload->>'ID' IS DISTINCT FROM NEW.id OR
           NEW.payload->>'TenantID' IS DISTINCT FROM NEW.tenant_id OR
           NEW.payload->>'ProjectID' IS DISTINCT FROM NEW.project_id OR
           NEW.payload->>'Name' IS DISTINCT FROM NEW.name OR
           NEW.payload->>'Lifecycle' IS DISTINCT FROM NEW.lifecycle OR
           (NEW.payload->>'Version')::bigint IS DISTINCT FROM NEW.version THEN
            RAISE EXCEPTION 'application payload is inconsistent with authoritative columns' USING ERRCODE = '23514';
        END IF;
    ELSIF TG_TABLE_NAME = 'environments' THEN
        IF NEW.payload->>'ID' IS DISTINCT FROM NEW.id OR
           NEW.payload->>'TenantID' IS DISTINCT FROM NEW.tenant_id OR
           NEW.payload->>'ApplicationID' IS DISTINCT FROM NEW.application_id OR
           NEW.payload->>'Name' IS DISTINCT FROM NEW.name OR
           NEW.payload->>'Namespace' IS DISTINCT FROM NEW.namespace OR
           (NEW.payload->>'Default')::boolean IS DISTINCT FROM NEW.is_default OR
           NEW.payload->>'ActiveReleaseID' IS DISTINCT FROM NEW.active_release_id OR
           NEW.payload->>'PlacementID' IS DISTINCT FROM NEW.placement_id OR
           (NEW.payload->>'Version')::bigint IS DISTINCT FROM NEW.version THEN
            RAISE EXCEPTION 'environment payload is inconsistent with authoritative columns' USING ERRCODE = '23514';
        END IF;
    ELSIF TG_TABLE_NAME = 'runtime_cells' THEN
        IF NEW.payload->>'ID' IS DISTINCT FROM NEW.id OR
           NEW.payload->>'Region' IS DISTINCT FROM NEW.region OR
           NEW.payload->>'State' IS DISTINCT FROM NEW.state OR
           (NEW.payload->>'CapacityUnits')::integer IS DISTINCT FROM NEW.capacity_units OR
           (NEW.payload->>'AllocatedUnits')::integer IS DISTINCT FROM NEW.allocated_units OR
           NEW.payload->>'GitOpsRepository' IS DISTINCT FROM NEW.gitops_repository OR
           (NEW.payload->>'Version')::bigint IS DISTINCT FROM NEW.version THEN
            RAISE EXCEPTION 'runtime cell payload is inconsistent with authoritative columns' USING ERRCODE = '23514';
        END IF;
    ELSIF TG_TABLE_NAME = 'placements' THEN
        IF NEW.payload->>'ID' IS DISTINCT FROM NEW.id OR
           NEW.payload->>'TenantID' IS DISTINCT FROM NEW.tenant_id OR
           NEW.payload->>'EnvironmentID' IS DISTINCT FROM NEW.environment_id OR
           NEW.payload->>'CellID' IS DISTINCT FROM NEW.cell_id OR
           (NEW.payload->>'Current')::boolean IS DISTINCT FROM NEW.current OR
           (NEW.payload->>'Units')::integer IS DISTINCT FROM NEW.units OR
           (NEW.payload->>'Version')::bigint IS DISTINCT FROM NEW.version THEN
            RAISE EXCEPTION 'placement payload is inconsistent with authoritative columns' USING ERRCODE = '23514';
        END IF;
    ELSIF TG_TABLE_NAME = 'placement_migrations' THEN
        IF NEW.payload->>'ID' IS DISTINCT FROM NEW.id OR
           NEW.payload->>'TenantID' IS DISTINCT FROM NEW.tenant_id OR
           NEW.payload->>'EnvironmentID' IS DISTINCT FROM NEW.environment_id OR
           NEW.payload->>'FromPlacementID' IS DISTINCT FROM NEW.from_placement_id OR
           NEW.payload->>'TargetCellID' IS DISTINCT FROM NEW.target_cell_id OR
           NEW.payload->>'State' IS DISTINCT FROM NEW.state THEN
            RAISE EXCEPTION 'placement migration payload is inconsistent with authoritative columns' USING ERRCODE = '23514';
        END IF;
    ELSIF TG_TABLE_NAME = 'releases' THEN
        IF NEW.payload->>'ID' IS DISTINCT FROM NEW.id OR
           NEW.payload->>'TenantID' IS DISTINCT FROM NEW.tenant_id OR
           NEW.payload->>'ApplicationID' IS DISTINCT FROM NEW.application_id OR
           NEW.payload->>'EnvironmentID' IS DISTINCT FROM NEW.environment_id OR
           NEW.payload->>'Identity' IS DISTINCT FROM NEW.identity OR
           NEW.payload->>'RollbackOf' IS DISTINCT FROM NEW.rollback_of OR
           NEW.payload#>>'{Artifact,artifact_id}' IS DISTINCT FROM NEW.artifact_id OR
           NEW.payload#>>'{Artifact,repository}' IS DISTINCT FROM NEW.repository OR
           NEW.payload#>>'{Artifact,digest}' IS DISTINCT FROM NEW.digest OR
           NEW.payload#>>'{Artifact,media_type}' IS DISTINCT FROM NEW.media_type OR
           NEW.payload->>'State' IS DISTINCT FROM NEW.state OR
           (NEW.payload->>'Version')::bigint IS DISTINCT FROM NEW.version THEN
            RAISE EXCEPTION 'release payload is inconsistent with authoritative columns' USING ERRCODE = '23514';
        END IF;
    ELSIF TG_TABLE_NAME = 'deployments' THEN
        IF NEW.payload->>'ID' IS DISTINCT FROM NEW.id OR
           NEW.payload->>'TenantID' IS DISTINCT FROM NEW.tenant_id OR
           NEW.payload->>'ApplicationID' IS DISTINCT FROM NEW.application_id OR
           NEW.payload->>'EnvironmentID' IS DISTINCT FROM NEW.environment_id OR
           NEW.payload->>'ReleaseID' IS DISTINCT FROM NEW.release_id OR
           NEW.payload->>'PlacementID' IS DISTINCT FROM NEW.placement_id OR
           NEW.payload->>'Phase' IS DISTINCT FROM NEW.phase OR
           NEW.payload->>'GitCommitSHA' IS DISTINCT FROM NEW.git_commit_sha OR
           (NEW.payload->>'Version')::bigint IS DISTINCT FROM NEW.version THEN
            RAISE EXCEPTION 'deployment payload is inconsistent with authoritative columns' USING ERRCODE = '23514';
        END IF;
    ELSIF TG_TABLE_NAME = 'gitops_commits' THEN
        IF NEW.payload->>'ID' IS DISTINCT FROM NEW.id OR
           NEW.payload->>'TenantID' IS DISTINCT FROM NEW.tenant_id OR
           NEW.payload->>'CellID' IS DISTINCT FROM NEW.cell_id OR
           NEW.payload->>'ReleaseID' IS DISTINCT FROM NEW.release_id OR
           NEW.payload->>'DeploymentID' IS DISTINCT FROM NEW.deployment_id OR
           NEW.payload->>'Path' IS DISTINCT FROM NEW.path OR
           NEW.payload->>'ManifestHash' IS DISTINCT FROM NEW.manifest_hash OR
           NEW.payload->>'CommitSHA' IS DISTINCT FROM NEW.commit_sha THEN
            RAISE EXCEPTION 'GitOps payload is inconsistent with authoritative columns' USING ERRCODE = '23514';
        END IF;
    ELSIF TG_TABLE_NAME = 'quarantine' THEN
        IF NEW.payload->>'ID' IS DISTINCT FROM NEW.id OR
           NEW.payload->>'CellID' IS DISTINCT FROM NEW.cell_id OR
           NEW.payload->>'Namespace' IS DISTINCT FROM NEW.namespace OR
           NEW.payload->>'Kind' IS DISTINCT FROM NEW.kind OR
           NEW.payload->>'Name' IS DISTINCT FROM NEW.name OR
           NEW.payload->>'Reason' IS DISTINCT FROM NEW.reason THEN
            RAISE EXCEPTION 'quarantine payload is inconsistent with authoritative columns' USING ERRCODE = '23514';
        END IF;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS runtime_application_payload_consistency ON runtime.applications;
CREATE TRIGGER runtime_application_payload_consistency BEFORE INSERT OR UPDATE ON runtime.applications FOR EACH ROW EXECUTE FUNCTION runtime.guard_payload_consistency();
DROP TRIGGER IF EXISTS runtime_environment_payload_consistency ON runtime.environments;
CREATE TRIGGER runtime_environment_payload_consistency BEFORE INSERT OR UPDATE ON runtime.environments FOR EACH ROW EXECUTE FUNCTION runtime.guard_payload_consistency();
DROP TRIGGER IF EXISTS runtime_cell_payload_consistency ON runtime.runtime_cells;
CREATE TRIGGER runtime_cell_payload_consistency BEFORE INSERT OR UPDATE ON runtime.runtime_cells FOR EACH ROW EXECUTE FUNCTION runtime.guard_payload_consistency();
DROP TRIGGER IF EXISTS runtime_placement_payload_consistency ON runtime.placements;
CREATE TRIGGER runtime_placement_payload_consistency BEFORE INSERT OR UPDATE ON runtime.placements FOR EACH ROW EXECUTE FUNCTION runtime.guard_payload_consistency();
DROP TRIGGER IF EXISTS runtime_placement_migration_payload_consistency ON runtime.placement_migrations;
CREATE TRIGGER runtime_placement_migration_payload_consistency BEFORE INSERT OR UPDATE ON runtime.placement_migrations FOR EACH ROW EXECUTE FUNCTION runtime.guard_payload_consistency();
DROP TRIGGER IF EXISTS runtime_release_payload_consistency ON runtime.releases;
CREATE TRIGGER runtime_release_payload_consistency BEFORE INSERT OR UPDATE ON runtime.releases FOR EACH ROW EXECUTE FUNCTION runtime.guard_payload_consistency();
DROP TRIGGER IF EXISTS runtime_deployment_payload_consistency ON runtime.deployments;
CREATE TRIGGER runtime_deployment_payload_consistency BEFORE INSERT OR UPDATE ON runtime.deployments FOR EACH ROW EXECUTE FUNCTION runtime.guard_payload_consistency();
DROP TRIGGER IF EXISTS runtime_gitops_payload_consistency ON runtime.gitops_commits;
CREATE TRIGGER runtime_gitops_payload_consistency BEFORE INSERT OR UPDATE ON runtime.gitops_commits FOR EACH ROW EXECUTE FUNCTION runtime.guard_payload_consistency();
DROP TRIGGER IF EXISTS runtime_quarantine_payload_consistency ON runtime.quarantine;
CREATE TRIGGER runtime_quarantine_payload_consistency BEFORE INSERT OR UPDATE ON runtime.quarantine FOR EACH ROW EXECUTE FUNCTION runtime.guard_payload_consistency();

CREATE OR REPLACE FUNCTION runtime.guard_runtime_cell_identity() RETURNS trigger AS $$
BEGIN
    IF NEW.id <> OLD.id OR NEW.region <> OLD.region OR NEW.gitops_repository <> OLD.gitops_repository OR
       NEW.payload->'Isolation' IS DISTINCT FROM OLD.payload->'Isolation' OR
       NEW.payload->>'GitOpsRevision' IS DISTINCT FROM OLD.payload->>'GitOpsRevision' OR
       NEW.payload->>'ClusterServer' IS DISTINCT FROM OLD.payload->>'ClusterServer' OR
       NEW.payload->>'ArgoProject' IS DISTINCT FROM OLD.payload->>'ArgoProject' OR
       NEW.payload->>'IngressDomain' IS DISTINCT FROM OLD.payload->>'IngressDomain' OR
       NEW.payload->>'CreatedAt' IS DISTINCT FROM OLD.payload->>'CreatedAt' THEN
        RAISE EXCEPTION 'runtime cell identity and endpoints are immutable' USING ERRCODE = '55000';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
DROP TRIGGER IF EXISTS runtime_cell_identity_immutable ON runtime.runtime_cells;
CREATE TRIGGER runtime_cell_identity_immutable BEFORE UPDATE ON runtime.runtime_cells FOR EACH ROW EXECUTE FUNCTION runtime.guard_runtime_cell_identity();

CREATE OR REPLACE FUNCTION runtime.guard_placement_identity() RETURNS trigger AS $$
BEGIN
    IF NEW.id <> OLD.id OR NEW.tenant_id <> OLD.tenant_id OR NEW.environment_id <> OLD.environment_id OR NEW.cell_id <> OLD.cell_id OR
       NEW.payload->>'Region' IS DISTINCT FROM OLD.payload->>'Region' OR
       NEW.payload->>'Isolation' IS DISTINCT FROM OLD.payload->>'Isolation' OR
       NEW.payload->>'CreatedAt' IS DISTINCT FROM OLD.payload->>'CreatedAt' THEN
        RAISE EXCEPTION 'placement identity is immutable' USING ERRCODE = '55000';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
DROP TRIGGER IF EXISTS runtime_placement_identity_immutable ON runtime.placements;
CREATE TRIGGER runtime_placement_identity_immutable BEFORE UPDATE ON runtime.placements FOR EACH ROW EXECUTE FUNCTION runtime.guard_placement_identity();

CREATE OR REPLACE FUNCTION runtime.guard_release_identity() RETURNS trigger AS $$
BEGIN
    IF NEW.id <> OLD.id OR NEW.tenant_id <> OLD.tenant_id OR NEW.application_id <> OLD.application_id OR NEW.environment_id <> OLD.environment_id OR
       NEW.identity <> OLD.identity OR NEW.artifact_id <> OLD.artifact_id OR NEW.repository <> OLD.repository OR
       NEW.digest <> OLD.digest OR NEW.media_type <> OLD.media_type OR NEW.rollback_of <> OLD.rollback_of OR
       NEW.payload->'Artifact' IS DISTINCT FROM OLD.payload->'Artifact' OR
       NEW.payload->'Configuration' IS DISTINCT FROM OLD.payload->'Configuration' OR
       NEW.payload->>'PolicyVersion' IS DISTINCT FROM OLD.payload->>'PolicyVersion' OR
       NEW.payload->>'RollbackOf' IS DISTINCT FROM OLD.payload->>'RollbackOf' OR
       NEW.payload->>'PolicyOverride' IS DISTINCT FROM OLD.payload->>'PolicyOverride' OR
       NEW.payload->>'CreatedAt' IS DISTINCT FROM OLD.payload->>'CreatedAt' THEN
        RAISE EXCEPTION 'release identity, artifact and configuration are immutable' USING ERRCODE = '55000';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
DROP TRIGGER IF EXISTS runtime_release_identity_immutable ON runtime.releases;
CREATE TRIGGER runtime_release_identity_immutable BEFORE UPDATE ON runtime.releases FOR EACH ROW EXECUTE FUNCTION runtime.guard_release_identity();

CREATE OR REPLACE FUNCTION runtime.guard_deployment_identity() RETURNS trigger AS $$
BEGIN
    IF NEW.id <> OLD.id OR NEW.tenant_id <> OLD.tenant_id OR NEW.application_id <> OLD.application_id OR NEW.environment_id <> OLD.environment_id OR
       NEW.release_id <> OLD.release_id OR NEW.placement_id <> OLD.placement_id OR
       NEW.payload->>'PreviousRelease' IS DISTINCT FROM OLD.payload->>'PreviousRelease' OR
       NEW.payload->>'CreatedAt' IS DISTINCT FROM OLD.payload->>'CreatedAt' THEN
        RAISE EXCEPTION 'deployment identity is immutable' USING ERRCODE = '55000';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
DROP TRIGGER IF EXISTS runtime_deployment_identity_immutable ON runtime.deployments;
CREATE TRIGGER runtime_deployment_identity_immutable BEFORE UPDATE ON runtime.deployments FOR EACH ROW EXECUTE FUNCTION runtime.guard_deployment_identity();
