package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/keir-research/ai-native-paas/internal/runtime/domain"
	buildv1 "github.com/keir-research/ai-native-paas/pkg/contracts/build/v1"
	runtimev1 "github.com/keir-research/ai-native-paas/pkg/contracts/runtime/v1"
)

type Service struct {
	Store        Store
	Artifacts    buildv1.ArtifactPolicy
	Renderer     Renderer
	GitOps       GitOpsRepository
	Observer     RuntimeObserver
	Units        UnitCatalog
	Clock        Clock
	IDs          IDGenerator
	Scheduler    PlacementScheduler
	AfterGitPush func() error
}

func (s Service) validateBase() error {
	if s.Store == nil || s.Clock == nil || s.IDs == nil {
		return domain.NewError(domain.CodeUnavailable, "runtime service dependencies are incomplete")
	}
	return nil
}

func requestHash(v any) string {
	raw, _ := json.Marshal(v)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func releaseRequestHash(req runtimev1.DeployRequest) string {
	return requestHash(struct {
		ApplicationID string                  `json:"application_id"`
		EnvironmentID string                  `json:"environment_id"`
		Artifact      buildv1.ArtifactRef     `json:"artifact"`
		Configuration runtimev1.ReleaseConfig `json:"configuration"`
	}{req.ApplicationID, req.EnvironmentID, req.Artifact, req.Configuration})
}

// requestedUnits computes the baseline capacity reservation for a release. A
// unit is charged per minimum process replica; autoscaling headroom above the
// minimum is governed separately at runtime. Keeping this calculation in the
// application layer makes scheduler and billing inputs deterministic.
func requestedUnits(catalog UnitCatalog, config runtimev1.ReleaseConfig) (int, error) {
	if catalog == nil {
		return 0, domain.NewError(domain.CodeUnavailable, "runtime unit catalog is not configured")
	}
	weight, ok := catalog.Units(config.Unit)
	if !ok || weight <= 0 {
		return 0, domain.NewError(domain.CodeInvalidArgument, "unknown runtime unit")
	}
	maxInt := int(^uint(0) >> 1)
	replicas := 0
	for _, process := range config.Processes {
		if process.MinReplicas < 0 || replicas > maxInt-process.MinReplicas {
			return 0, domain.NewError(domain.CodeInvalidArgument, "runtime capacity request overflows")
		}
		replicas += process.MinReplicas
	}
	if replicas <= 0 || weight > maxInt/replicas {
		return 0, domain.NewError(domain.CodeInvalidArgument, "runtime capacity request is invalid")
	}
	return weight * replicas, nil
}

func resizePlacement(tx Tx, placement domain.Placement, units int, now time.Time) (domain.Placement, error) {
	if placement.Units == units {
		return placement, nil
	}
	cell, ok := tx.GetRuntimeCell(placement.CellID)
	if !ok {
		return domain.Placement{}, domain.NewError(domain.CodeInternal, "placement points to missing runtime cell")
	}
	expectedCell, expectedPlacement := cell.Version, placement.Version
	delta := units - placement.Units
	if delta > 0 {
		if err := cell.Allocate(delta, now); err != nil {
			return domain.Placement{}, err
		}
	} else if err := cell.Release(-delta, now); err != nil {
		return domain.Placement{}, err
	}
	if err := placement.Resize(units, now); err != nil {
		return domain.Placement{}, err
	}
	if err := tx.UpdateRuntimeCell(cell, expectedCell); err != nil {
		return domain.Placement{}, err
	}
	if err := tx.UpdatePlacement(placement, expectedPlacement); err != nil {
		return domain.Placement{}, err
	}
	return placement, nil
}

func (s Service) replayCreateRelease(ctx context.Context, req runtimev1.DeployRequest) (domain.Release, domain.Placement, bool, error) {
	var release domain.Release
	var placement domain.Placement
	var replay bool
	err := s.Store.Transact(ctx, func(tx Tx) error {
		record, found := tx.GetIdempotency(req.TenantID, "runtime.release.create", req.IdempotencyKey)
		if !found {
			return nil
		}
		app, ok := tx.GetApplication(req.ApplicationID)
		if !ok || app.TenantID != req.TenantID {
			return domain.NewError(domain.CodeNotFound, "application not found")
		}
		env, ok := tx.GetEnvironment(req.EnvironmentID)
		if !ok || env.ApplicationID != app.ID || env.TenantID != req.TenantID {
			return domain.NewError(domain.CodeNotFound, "environment not found")
		}
		placement, ok = tx.GetCurrentPlacement(env.ID)
		if !ok {
			return domain.NewError(domain.CodeInternal, "release exists without placement")
		}
		cell, ok := tx.GetRuntimeCell(placement.CellID)
		if !ok {
			return domain.NewError(domain.CodeInternal, "placement points to missing runtime cell")
		}
		hostname, err := domain.GeneratedHostname(app.ID, env.Name, cell.IngressDomain)
		if err != nil {
			return err
		}
		req.Configuration.GeneratedHostname = hostname
		if record.RequestHash != releaseRequestHash(req) {
			return domain.NewError(domain.CodeConflict, "idempotency key reused with different release request")
		}
		release, ok = tx.GetRelease(record.ResourceID)
		if !ok || release.ApplicationID != app.ID || release.EnvironmentID != env.ID {
			return domain.NewError(domain.CodeInternal, "release idempotency record points to invalid release")
		}
		replay = true
		return nil
	})
	return release, placement, replay, err
}

func (s Service) CreateApplication(ctx context.Context, req CreateApplicationRequest) (domain.Application, domain.Environment, error) {
	if err := s.validateBase(); err != nil {
		return domain.Application{}, domain.Environment{}, err
	}
	req.TenantID, req.ProjectID, req.Name, req.ActorID, req.IdempotencyKey = strings.TrimSpace(req.TenantID), strings.TrimSpace(req.ProjectID), strings.TrimSpace(req.Name), strings.TrimSpace(req.ActorID), strings.TrimSpace(req.IdempotencyKey)
	if req.TenantID == "" || req.ProjectID == "" || req.Name == "" || req.ActorID == "" || req.IdempotencyKey == "" {
		return domain.Application{}, domain.Environment{}, domain.NewError(domain.CodeInvalidArgument, "application request is incomplete")
	}
	now := s.Clock.Now()
	hash := requestHash(req)
	var app domain.Application
	var env domain.Environment
	err := s.Store.Transact(ctx, func(tx Tx) error {
		if record, ok := tx.GetIdempotency(req.TenantID, "runtime.application.create", req.IdempotencyKey); ok {
			if record.RequestHash != hash {
				return domain.NewError(domain.CodeConflict, "idempotency key reused with different request")
			}
			var found bool
			app, found = tx.GetApplication(record.ResourceID)
			if !found {
				return domain.NewError(domain.CodeInternal, "idempotency record points to missing application")
			}
			env, found = tx.FindEnvironmentByApplicationName(app.ID, "production")
			if !found {
				return domain.NewError(domain.CodeInternal, "default environment is missing")
			}
			return nil
		}
		if _, ok := tx.FindApplicationByTenantName(req.TenantID, strings.ToLower(req.Name)); ok {
			return domain.NewError(domain.CodeConflict, "application name already exists")
		}
		var err error
		app, err = domain.NewApplication(s.IDs.New("app"), req.TenantID, req.ProjectID, req.Name, now)
		if err != nil {
			return err
		}
		env, err = domain.NewEnvironment(s.IDs.New("env"), req.TenantID, app.ID, "production", true, now)
		if err != nil {
			return err
		}
		if err = tx.InsertApplication(app); err != nil {
			return err
		}
		if err = tx.InsertEnvironment(env); err != nil {
			return err
		}
		if err = tx.InsertIdempotency(domain.IdempotencyRecord{TenantID: req.TenantID, Scope: "runtime.application.create", Key: req.IdempotencyKey, RequestHash: hash, ResourceID: app.ID, CreatedAt: now}); err != nil {
			return err
		}
		if err = tx.AppendOutbox(domain.OutboxRecord{ID: s.IDs.New("evt"), TenantID: req.TenantID, Topic: "runtime.application.created.v1", AggregateID: app.ID, Payload: mustJSON(map[string]string{"application_id": app.ID, "environment_id": env.ID}), CreatedAt: now}); err != nil {
			return err
		}
		return tx.AppendAudit(domain.AuditRecord{ID: s.IDs.New("aud"), TenantID: req.TenantID, ActorID: req.ActorID, Action: "runtime.application.create", ResourceType: "application", ResourceID: app.ID, Data: mustJSON(map[string]string{"project_id": req.ProjectID, "name": req.Name}), CreatedAt: now})
	})
	return app, env, err
}

func (s Service) CreateEnvironment(ctx context.Context, req CreateEnvironmentRequest) (domain.Environment, error) {
	if err := s.validateBase(); err != nil {
		return domain.Environment{}, err
	}
	if strings.TrimSpace(req.TenantID) == "" || strings.TrimSpace(req.ApplicationID) == "" || strings.TrimSpace(req.Name) == "" || strings.TrimSpace(req.ActorID) == "" || strings.TrimSpace(req.IdempotencyKey) == "" {
		return domain.Environment{}, domain.NewError(domain.CodeInvalidArgument, "environment request is incomplete")
	}
	now, hash := s.Clock.Now(), requestHash(req)
	var env domain.Environment
	err := s.Store.Transact(ctx, func(tx Tx) error {
		if record, ok := tx.GetIdempotency(req.TenantID, "runtime.environment.create", req.IdempotencyKey); ok {
			if record.RequestHash != hash {
				return domain.NewError(domain.CodeConflict, "idempotency key reused with different request")
			}
			var found bool
			env, found = tx.GetEnvironment(record.ResourceID)
			if !found {
				return domain.NewError(domain.CodeInternal, "idempotency record points to missing environment")
			}
			return nil
		}
		app, ok := tx.GetApplication(req.ApplicationID)
		if !ok || app.TenantID != req.TenantID {
			return domain.NewError(domain.CodeNotFound, "application not found")
		}
		if _, ok = tx.FindEnvironmentByApplicationName(req.ApplicationID, strings.ToLower(strings.TrimSpace(req.Name))); ok {
			return domain.NewError(domain.CodeConflict, "environment name already exists")
		}
		if req.Default {
			for _, existing := range tx.ListEnvironmentsByApplication(req.ApplicationID) {
				if existing.Default {
					return domain.NewError(domain.CodeConflict, "default environment already exists")
				}
			}
		}
		var err error
		env, err = domain.NewEnvironment(s.IDs.New("env"), req.TenantID, req.ApplicationID, req.Name, req.Default, now)
		if err != nil {
			return err
		}
		if err = tx.InsertEnvironment(env); err != nil {
			return err
		}
		return tx.InsertIdempotency(domain.IdempotencyRecord{TenantID: req.TenantID, Scope: "runtime.environment.create", Key: req.IdempotencyKey, RequestHash: hash, ResourceID: env.ID, CreatedAt: now})
	})
	return env, err
}

func (s Service) RegisterCell(ctx context.Context, req RegisterCellRequest) (domain.RuntimeCell, error) {
	if err := s.validateBase(); err != nil {
		return domain.RuntimeCell{}, err
	}
	now := s.Clock.Now()
	cell, err := domain.NewRuntimeCell(req.ID, req.Region, req.Isolation, req.CapacityUnits, req.GitOpsRepository, req.ClusterServer, req.ArgoProject, req.IngressDomain, now)
	if err != nil {
		return domain.RuntimeCell{}, err
	}
	err = s.Store.Transact(ctx, func(tx Tx) error {
		if existing, ok := tx.GetRuntimeCell(cell.ID); ok {
			if existing.SameConfiguration(cell) {
				cell = existing
				return nil
			}
			return domain.NewError(domain.CodeConflict, "runtime cell id already exists with different configuration")
		}
		return tx.InsertRuntimeCell(cell)
	})
	return cell, err
}

func (s Service) CreateRelease(ctx context.Context, req runtimev1.DeployRequest) (domain.Release, domain.Placement, error) {
	if err := s.validateBase(); err != nil {
		return domain.Release{}, domain.Placement{}, err
	}
	if s.Artifacts == nil || s.Units == nil {
		return domain.Release{}, domain.Placement{}, domain.NewError(domain.CodeUnavailable, "artifact policy or unit catalog is not configured")
	}
	if err := req.Validate(); err != nil {
		return domain.Release{}, domain.Placement{}, domain.NewError(domain.CodeInvalidArgument, err.Error())
	}
	// Generated hostnames are platform-owned. Ignore the caller value before
	// validation so it cannot influence identity, routing, or idempotency.
	req.Configuration.GeneratedHostname = ""
	normalized, err := domain.NormalizeReleaseConfig(req.Configuration)
	if err != nil {
		return domain.Release{}, domain.Placement{}, err
	}
	req.Configuration = normalized
	if release, placement, replay, replayErr := s.replayCreateRelease(ctx, req); replayErr != nil {
		return domain.Release{}, domain.Placement{}, replayErr
	} else if replay {
		return release, placement, nil
	}
	units, err := requestedUnits(s.Units, req.Configuration)
	if err != nil {
		return domain.Release{}, domain.Placement{}, err
	}
	decision, err := s.Artifacts.IsReleasable(ctx, req.Artifact)
	if err != nil {
		return domain.Release{}, domain.Placement{}, err
	}
	if !decision.Allowed {
		return domain.Release{}, domain.Placement{}, domain.NewError(domain.CodePolicyRejected, "artifact is not releasable: "+strings.Join(decision.Reasons, "; "))
	}
	now := s.Clock.Now()
	var release domain.Release
	var placement domain.Placement
	err = s.Store.Transact(ctx, func(tx Tx) error {
		app, ok := tx.GetApplication(req.ApplicationID)
		if !ok || app.TenantID != req.TenantID {
			return domain.NewError(domain.CodeNotFound, "application not found")
		}
		env, ok := tx.GetEnvironment(req.EnvironmentID)
		if !ok || env.ApplicationID != app.ID || env.TenantID != req.TenantID {
			return domain.NewError(domain.CodeNotFound, "environment not found")
		}

		// Resolve placement before hashing the effective request. Generated
		// hostnames are platform-owned and therefore derived from the selected
		// cell instead of trusting a caller-provided route.
		var cell domain.RuntimeCell
		placementExists := false
		if current, found := tx.GetCurrentPlacement(env.ID); found {
			placement = current
			placementExists = true
			cell, ok = tx.GetRuntimeCell(current.CellID)
			if !ok {
				return domain.NewError(domain.CodeInternal, "placement points to missing runtime cell")
			}
			if current.Region != req.Configuration.Region || current.Isolation != req.Configuration.Isolation {
				return domain.NewError(domain.CodeConflict, "region or isolation change requires explicit placement migration")
			}
		} else {
			cell, err = s.Scheduler.Select(tx.ListRuntimeCells(), req.Configuration.Region, req.Configuration.Isolation, units)
			if err != nil {
				return err
			}
		}
		hostname, err := domain.GeneratedHostname(app.ID, env.Name, cell.IngressDomain)
		if err != nil {
			return err
		}
		req.Configuration.GeneratedHostname = hostname

		commandHash := releaseRequestHash(req)
		if record, found := tx.GetIdempotency(req.TenantID, "runtime.release.create", req.IdempotencyKey); found {
			if record.RequestHash != commandHash {
				return domain.NewError(domain.CodeConflict, "idempotency key reused with different release request")
			}
			var foundRelease bool
			release, foundRelease = tx.GetRelease(record.ResourceID)
			if !foundRelease {
				return domain.NewError(domain.CodeInternal, "release idempotency record points to missing release")
			}
			if current, foundPlacement := tx.GetCurrentPlacement(env.ID); foundPlacement {
				placement = current
				return nil
			}
			return domain.NewError(domain.CodeInternal, "release exists without placement")
		}
		identity, err := domain.ReleaseIdentity(env.ID, req.Artifact, req.Configuration)
		if err != nil {
			return err
		}
		if existing, found := tx.FindReleaseByIdentity(req.TenantID, env.ID, identity); found {
			release = existing
			if current, ok := tx.GetCurrentPlacement(env.ID); ok {
				placement = current
				return tx.InsertIdempotency(domain.IdempotencyRecord{TenantID: req.TenantID, Scope: "runtime.release.create", Key: req.IdempotencyKey, RequestHash: commandHash, ResourceID: release.ID, CreatedAt: now})
			}
			return domain.NewError(domain.CodeInternal, "release exists without placement")
		}
		if placementExists {
			placement, err = resizePlacement(tx, placement, units, now)
			if err != nil {
				return err
			}
		} else {
			placement, err = domain.NewPlacement(s.IDs.New("plc"), req.TenantID, env.ID, cell, req.Configuration.Isolation, units, now)
			if err != nil {
				return err
			}
			expectedCell := cell.Version
			if err = cell.Allocate(units, now); err != nil {
				return err
			}
			if err = tx.UpdateRuntimeCell(cell, expectedCell); err != nil {
				return err
			}
			if err = tx.InsertPlacement(placement); err != nil {
				return err
			}
			expectedEnv := env.Version
			if err = env.SetPlacement(placement.ID, now); err != nil {
				return err
			}
			if err = tx.UpdateEnvironment(env, expectedEnv); err != nil {
				return err
			}
		}
		release, err = domain.NewRelease(s.IDs.New("rel"), req.TenantID, app.ID, env.ID, req.Artifact, req.Configuration, decision.PolicyVersion, now)
		if err != nil {
			return err
		}
		if err = release.Transition(runtimev1.ReleaseValidated, now); err != nil {
			return err
		}
		if err = tx.InsertRelease(release); err != nil {
			return err
		}
		if err = tx.InsertIdempotency(domain.IdempotencyRecord{TenantID: req.TenantID, Scope: "runtime.release.create", Key: req.IdempotencyKey, RequestHash: commandHash, ResourceID: release.ID, CreatedAt: now}); err != nil {
			return err
		}
		return tx.AppendAudit(domain.AuditRecord{ID: s.IDs.New("aud"), TenantID: req.TenantID, ActorID: req.ActorID, Action: "runtime.release.create", ResourceType: "release", ResourceID: release.ID, Data: mustJSON(map[string]string{"artifact_id": req.Artifact.ArtifactID, "digest": req.Artifact.Digest}), CreatedAt: now})
	})
	return release, placement, err
}

func (s Service) Deploy(ctx context.Context, req runtimev1.DeployRequest) (runtimev1.DeploymentRef, error) {
	if s.Renderer == nil || s.GitOps == nil {
		return runtimev1.DeploymentRef{}, domain.NewError(domain.CodeUnavailable, "GitOps dependencies are incomplete")
	}
	release, placement, err := s.CreateRelease(ctx, req)
	if err != nil {
		return runtimev1.DeploymentRef{}, err
	}
	now := s.Clock.Now()
	var app domain.Application
	var env domain.Environment
	var cell domain.RuntimeCell
	var deployment domain.Deployment
	err = s.Store.Transact(ctx, func(tx Tx) error {
		var ok bool
		app, ok = tx.GetApplication(release.ApplicationID)
		if !ok {
			return domain.NewError(domain.CodeInternal, "application missing")
		}
		env, ok = tx.GetEnvironment(release.EnvironmentID)
		if !ok {
			return domain.NewError(domain.CodeInternal, "environment missing")
		}
		cell, ok = tx.GetRuntimeCell(placement.CellID)
		if !ok {
			return domain.NewError(domain.CodeInternal, "runtime cell missing")
		}
		if existing, found := tx.FindDeploymentByRelease(release.ID); found {
			deployment = existing
			return nil
		}
		var err error
		deployment, err = domain.NewDeployment(s.IDs.New("dep"), release, placement, env.ActiveReleaseID, now)
		if err != nil {
			return err
		}
		return tx.InsertDeployment(deployment)
	})
	if err != nil {
		return runtimev1.DeploymentRef{}, err
	}
	if deployment.Phase != runtimev1.DeploymentPending {
		return runtimev1.DeploymentRef{DeploymentID: deployment.ID, ReleaseID: release.ID, Phase: deployment.Phase}, nil
	}
	bundle, err := s.Renderer.Render(RenderInput{Application: app, Environment: env, Release: release, Placement: placement, Cell: cell})
	if err != nil {
		return runtimev1.DeploymentRef{}, err
	}
	commit, err := s.GitOps.Commit(ctx, CommitRequest{Bundle: bundle, DeploymentID: deployment.ID, ActorID: req.ActorID})
	if err != nil {
		return runtimev1.DeploymentRef{}, err
	}
	if s.AfterGitPush != nil {
		if err := s.AfterGitPush(); err != nil {
			return runtimev1.DeploymentRef{}, err
		}
	}
	err = s.recordGitCommit(ctx, release.ID, deployment.ID, commit, req.ActorID)
	if err != nil {
		return runtimev1.DeploymentRef{}, err
	}
	return runtimev1.DeploymentRef{DeploymentID: deployment.ID, ReleaseID: release.ID, Phase: runtimev1.DeploymentGitCommitted}, nil
}

func (s Service) recordGitCommit(ctx context.Context, releaseID, deploymentID string, commit CommitResult, actorID string) error {
	now := s.Clock.Now()
	return s.Store.Transact(ctx, func(tx Tx) error {
		if existing, ok := tx.GetGitOpsCommitByRelease(releaseID); ok {
			if existing.CommitSHA != commit.CommitSHA || existing.ManifestHash != commit.ManifestHash {
				return domain.NewError(domain.CodeConflict, "release already points to a different GitOps commit")
			}
			return nil
		}
		release, ok := tx.GetRelease(releaseID)
		if !ok {
			return domain.NewError(domain.CodeNotFound, "release not found")
		}
		deployment, ok := tx.GetDeployment(deploymentID)
		if !ok {
			return domain.NewError(domain.CodeNotFound, "deployment not found")
		}
		placement, ok := tx.GetPlacement(deployment.PlacementID)
		if !ok {
			return domain.NewError(domain.CodeInternal, "placement not found")
		}
		if deployment.ReleaseID != release.ID {
			return domain.NewError(domain.CodeConflict, "deployment release mismatch")
		}
		expectedDep, expectedRel := deployment.Version, release.Version
		if err := deployment.RecordGitCommit(commit.CommitSHA, now); err != nil {
			return err
		}
		if err := release.Transition(runtimev1.ReleaseCommittedToGitOps, now); err != nil {
			return err
		}
		if err := tx.InsertGitOpsCommit(domain.GitOpsCommitRecord{ID: s.IDs.New("git"), TenantID: release.TenantID, CellID: placement.CellID, ReleaseID: release.ID, DeploymentID: deployment.ID, Path: commit.Path, ManifestHash: commit.ManifestHash, CommitSHA: commit.CommitSHA, CreatedAt: now}); err != nil {
			return err
		}
		if err := tx.UpdateDeployment(deployment, expectedDep); err != nil {
			return err
		}
		if err := tx.UpdateRelease(release, expectedRel); err != nil {
			return err
		}
		if err := tx.AppendOutbox(domain.OutboxRecord{ID: s.IDs.New("evt"), TenantID: release.TenantID, Topic: "runtime.release.gitops_committed.v1", AggregateID: release.ID, Payload: mustJSON(map[string]string{"commit_sha": commit.CommitSHA, "deployment_id": deployment.ID}), CreatedAt: now}); err != nil {
			return err
		}
		return tx.AppendAudit(domain.AuditRecord{ID: s.IDs.New("aud"), TenantID: release.TenantID, ActorID: actorID, Action: "runtime.release.commit_gitops", ResourceType: "release", ResourceID: release.ID, Data: mustJSON(map[string]string{"commit_sha": commit.CommitSHA}), CreatedAt: now})
	})
}

func (s Service) ReconcileGitOps(ctx context.Context, releaseID, actorID string) error {
	var deployment domain.Deployment
	var placement domain.Placement
	err := s.Store.Transact(ctx, func(tx Tx) error {
		if _, ok := tx.GetGitOpsCommitByRelease(releaseID); ok {
			return nil
		}
		var ok bool
		deployment, ok = tx.FindDeploymentByRelease(releaseID)
		if !ok {
			return domain.NewError(domain.CodeNotFound, "deployment not found")
		}
		placement, ok = tx.GetPlacement(deployment.PlacementID)
		if !ok {
			return domain.NewError(domain.CodeInternal, "placement not found")
		}
		return nil
	})
	if err != nil {
		return err
	}
	if deployment.ID == "" {
		return nil
	}
	commit, ok, err := s.GitOps.FindByRelease(ctx, placement.CellID, releaseID)
	if err != nil {
		return err
	}
	if !ok {
		return domain.NewError(domain.CodeNotFound, "GitOps commit not found")
	}
	return s.recordGitCommit(ctx, releaseID, deployment.ID, commit, actorID)
}

func (s Service) Rollback(ctx context.Context, environmentID, targetReleaseID string) (runtimev1.DeploymentRef, error) {
	return s.RollbackWithRequest(ctx, RollbackRequest{EnvironmentID: environmentID, TargetReleaseID: targetReleaseID, TenantID: "", ActorID: "system", IdempotencyKey: "rollback-" + targetReleaseID})
}

func (s Service) RollbackWithRequest(ctx context.Context, req RollbackRequest) (runtimev1.DeploymentRef, error) {
	if err := s.validateBase(); err != nil {
		return runtimev1.DeploymentRef{}, err
	}
	if s.Artifacts == nil || s.Renderer == nil || s.GitOps == nil {
		return runtimev1.DeploymentRef{}, domain.NewError(domain.CodeUnavailable, "rollback dependencies are incomplete")
	}
	req.TenantID = strings.TrimSpace(req.TenantID)
	req.EnvironmentID = strings.TrimSpace(req.EnvironmentID)
	req.TargetReleaseID = strings.TrimSpace(req.TargetReleaseID)
	req.ActorID = strings.TrimSpace(req.ActorID)
	req.IdempotencyKey = strings.TrimSpace(req.IdempotencyKey)
	if req.EnvironmentID == "" || req.TargetReleaseID == "" || req.ActorID == "" || req.IdempotencyKey == "" {
		return runtimev1.DeploymentRef{}, domain.NewError(domain.CodeInvalidArgument, "rollback request is incomplete")
	}

	var env domain.Environment
	var target domain.Release
	var app domain.Application
	var release domain.Release
	var placement domain.Placement
	var rollbackConfig runtimev1.ReleaseConfig
	var replay bool
	var rollbackHash string

	// Resolve tenant ownership and detect a replay before consulting a mutable
	// artifact policy. Once a rollback command has been accepted, a retry must
	// resume the same release rather than create another release or re-evaluate
	// a policy that may have changed in the meantime.
	err := s.Store.Transact(ctx, func(tx Tx) error {
		var ok bool
		env, ok = tx.GetEnvironment(req.EnvironmentID)
		if !ok || (req.TenantID != "" && env.TenantID != req.TenantID) {
			return domain.NewError(domain.CodeNotFound, "environment not found")
		}
		rollbackHash = requestHash(struct {
			EnvironmentID   string `json:"environment_id"`
			TargetReleaseID string `json:"target_release_id"`
			Critical        bool   `json:"critical_override"`
		}{req.EnvironmentID, req.TargetReleaseID, req.CriticalOverride})
		if record, found := tx.GetIdempotency(env.TenantID, "runtime.release.rollback", req.IdempotencyKey); found {
			if record.RequestHash != rollbackHash {
				return domain.NewError(domain.CodeConflict, "idempotency key reused with different rollback request")
			}
			release, ok = tx.GetRelease(record.ResourceID)
			if !ok || release.EnvironmentID != env.ID || release.RollbackOf == "" {
				return domain.NewError(domain.CodeInternal, "rollback idempotency record points to invalid release")
			}
			placement, ok = tx.GetCurrentPlacement(env.ID)
			if !ok {
				return domain.NewError(domain.CodeInternal, "placement not found")
			}
			app, ok = tx.GetApplication(env.ApplicationID)
			if !ok {
				return domain.NewError(domain.CodeInternal, "application not found")
			}
			replay = true
			return nil
		}
		target, ok = tx.GetRelease(req.TargetReleaseID)
		if !ok || target.EnvironmentID != env.ID {
			return domain.NewError(domain.CodeNotFound, "target release not found")
		}
		app, ok = tx.GetApplication(env.ApplicationID)
		if !ok {
			return domain.NewError(domain.CodeInternal, "application not found")
		}
		placement, ok = tx.GetCurrentPlacement(env.ID)
		if !ok {
			return domain.NewError(domain.CodeInternal, "placement not found")
		}
		cell, ok := tx.GetRuntimeCell(placement.CellID)
		if !ok {
			return domain.NewError(domain.CodeInternal, "placement points to missing runtime cell")
		}
		rollbackConfig = target.Configuration
		rollbackConfig.Region = placement.Region
		rollbackConfig.Isolation = placement.Isolation
		hostname, hostErr := domain.GeneratedHostname(app.ID, env.Name, cell.IngressDomain)
		if hostErr != nil {
			return hostErr
		}
		rollbackConfig.GeneratedHostname = hostname
		var normalizeErr error
		rollbackConfig, normalizeErr = domain.NormalizeReleaseConfig(rollbackConfig)
		return normalizeErr
	})
	if err != nil {
		return runtimev1.DeploymentRef{}, err
	}
	if replay {
		deployReq := runtimev1.DeployRequest{TenantID: env.TenantID, ApplicationID: app.ID, EnvironmentID: env.ID, Artifact: release.Artifact, Configuration: release.Configuration, IdempotencyKey: req.IdempotencyKey, ActorID: req.ActorID}
		return s.deployExistingRelease(ctx, deployReq, release, placement)
	}

	targetUnits, err := requestedUnits(s.Units, rollbackConfig)
	if err != nil {
		return runtimev1.DeploymentRef{}, err
	}
	decision, err := s.Artifacts.IsReleasable(ctx, target.Artifact)
	if err != nil {
		return runtimev1.DeploymentRef{}, err
	}
	if !decision.Allowed && !req.CriticalOverride {
		return runtimev1.DeploymentRef{}, domain.NewError(domain.CodePolicyRejected, "rollback artifact is no longer allowed")
	}
	now := s.Clock.Now()
	candidate, err := domain.NewRelease(s.IDs.New("rel"), env.TenantID, app.ID, env.ID, target.Artifact, rollbackConfig, decision.PolicyVersion, now)
	if err != nil {
		return runtimev1.DeploymentRef{}, err
	}
	candidate.RollbackOf = target.ID
	candidate.PolicyOverride = req.CriticalOverride
	if err = candidate.Transition(runtimev1.ReleaseValidated, now); err != nil {
		return runtimev1.DeploymentRef{}, err
	}

	// Recheck idempotency in the write transaction so concurrent retries have
	// a single winner. The unique persistence constraint is the final guard.
	err = s.Store.Transact(ctx, func(tx Tx) error {
		if record, found := tx.GetIdempotency(env.TenantID, "runtime.release.rollback", req.IdempotencyKey); found {
			if record.RequestHash != rollbackHash {
				return domain.NewError(domain.CodeConflict, "idempotency key reused with different rollback request")
			}
			var ok bool
			release, ok = tx.GetRelease(record.ResourceID)
			if !ok {
				return domain.NewError(domain.CodeInternal, "rollback idempotency record points to missing release")
			}
			placement, ok = tx.GetCurrentPlacement(env.ID)
			if !ok {
				return domain.NewError(domain.CodeInternal, "rollback release exists without placement")
			}
			return nil
		}
		current, ok := tx.GetCurrentPlacement(env.ID)
		if !ok || current.ID != placement.ID {
			return domain.NewError(domain.CodeConflict, "placement changed while rollback was being prepared")
		}
		if current.Region != candidate.Configuration.Region || current.Isolation != candidate.Configuration.Isolation {
			return domain.NewError(domain.CodeConflict, "rollback placement is no longer compatible")
		}
		current, err := resizePlacement(tx, current, targetUnits, now)
		if err != nil {
			return err
		}
		placement = current
		if err := tx.InsertRelease(candidate); err != nil {
			return err
		}
		if err := tx.InsertIdempotency(domain.IdempotencyRecord{TenantID: env.TenantID, Scope: "runtime.release.rollback", Key: req.IdempotencyKey, RequestHash: rollbackHash, ResourceID: candidate.ID, CreatedAt: now}); err != nil {
			return err
		}
		if err := tx.AppendAudit(domain.AuditRecord{ID: s.IDs.New("aud"), TenantID: env.TenantID, ActorID: req.ActorID, Action: "runtime.release.rollback", ResourceType: "release", ResourceID: candidate.ID, Data: mustJSON(map[string]string{"target_release_id": target.ID, "digest": target.Artifact.Digest}), CreatedAt: now}); err != nil {
			return err
		}
		release = candidate
		return nil
	})
	if err != nil {
		return runtimev1.DeploymentRef{}, err
	}
	deployReq := runtimev1.DeployRequest{TenantID: env.TenantID, ApplicationID: app.ID, EnvironmentID: env.ID, Artifact: release.Artifact, Configuration: release.Configuration, IdempotencyKey: req.IdempotencyKey, ActorID: req.ActorID}
	return s.deployExistingRelease(ctx, deployReq, release, placement)
}
func (s Service) deployExistingRelease(ctx context.Context, req runtimev1.DeployRequest, release domain.Release, placement domain.Placement) (runtimev1.DeploymentRef, error) {
	var app domain.Application
	var env domain.Environment
	var cell domain.RuntimeCell
	var deployment domain.Deployment
	err := s.Store.Transact(ctx, func(tx Tx) error {
		var ok bool
		app, ok = tx.GetApplication(release.ApplicationID)
		if !ok {
			return domain.NewError(domain.CodeInternal, "application missing")
		}
		env, ok = tx.GetEnvironment(release.EnvironmentID)
		if !ok {
			return domain.NewError(domain.CodeInternal, "environment missing")
		}
		cell, ok = tx.GetRuntimeCell(placement.CellID)
		if !ok {
			return domain.NewError(domain.CodeInternal, "cell missing")
		}
		if existing, found := tx.FindDeploymentByRelease(release.ID); found {
			deployment = existing
			return nil
		}
		var err error
		deployment, err = domain.NewDeployment(s.IDs.New("dep"), release, placement, env.ActiveReleaseID, s.Clock.Now())
		if err != nil {
			return err
		}
		return tx.InsertDeployment(deployment)
	})
	if err != nil {
		return runtimev1.DeploymentRef{}, err
	}
	if deployment.Phase != runtimev1.DeploymentPending {
		return runtimev1.DeploymentRef{DeploymentID: deployment.ID, ReleaseID: release.ID, Phase: deployment.Phase}, nil
	}
	bundle, err := s.Renderer.Render(RenderInput{Application: app, Environment: env, Release: release, Placement: placement, Cell: cell})
	if err != nil {
		return runtimev1.DeploymentRef{}, err
	}
	commit, err := s.GitOps.Commit(ctx, CommitRequest{Bundle: bundle, DeploymentID: deployment.ID, ActorID: req.ActorID})
	if err != nil {
		return runtimev1.DeploymentRef{}, err
	}
	if err = s.recordGitCommit(ctx, release.ID, deployment.ID, commit, req.ActorID); err != nil {
		return runtimev1.DeploymentRef{}, err
	}
	return runtimev1.DeploymentRef{DeploymentID: deployment.ID, ReleaseID: release.ID, Phase: runtimev1.DeploymentGitCommitted}, nil
}

func (s Service) RequestPlacementMigration(ctx context.Context, req ExplicitMigrationRequest) (domain.PlacementMigration, error) {
	if err := s.validateBase(); err != nil {
		return domain.PlacementMigration{}, err
	}
	now := s.Clock.Now()
	var operation domain.PlacementMigration
	err := s.Store.Transact(ctx, func(tx Tx) error {
		env, ok := tx.GetEnvironment(req.EnvironmentID)
		if !ok || (req.TenantID != "" && env.TenantID != req.TenantID) {
			return domain.NewError(domain.CodeNotFound, "environment not found")
		}
		current, ok := tx.GetCurrentPlacement(env.ID)
		if !ok {
			return domain.NewError(domain.CodeNotFound, "current placement not found")
		}
		target, ok := tx.GetRuntimeCell(req.TargetCellID)
		if !ok {
			return domain.NewError(domain.CodeNotFound, "target cell not found")
		}
		if target.ID == current.CellID {
			return domain.NewError(domain.CodeConflict, "target cell is already current")
		}
		if !target.CanPlace(current.Region, current.Isolation, current.Units) {
			return domain.NewError(domain.CodeCapacity, "target cell is not compatible")
		}
		operation = domain.PlacementMigration{ID: s.IDs.New("pmg"), TenantID: env.TenantID, EnvironmentID: env.ID, FromPlacementID: current.ID, TargetCellID: target.ID, State: "PENDING", CreatedAt: now}
		return tx.InsertPlacementMigration(operation)
	})
	return operation, err
}

func (s Service) Status(ctx context.Context, deploymentID string) (runtimev1.RuntimeStatus, error) {
	var value domain.Deployment
	err := s.Store.Transact(ctx, func(tx Tx) error {
		var ok bool
		value, ok = tx.GetDeployment(deploymentID)
		if !ok {
			return domain.NewError(domain.CodeNotFound, "deployment not found")
		}
		return nil
	})
	return runtimev1.RuntimeStatus{DeploymentID: value.ID, Phase: value.Phase, ActiveRelease: func() string {
		if value.Phase == runtimev1.DeploymentReady {
			return value.ReleaseID
		}
		return value.PreviousRelease
	}(), URL: value.URL, ReadyReplicas: value.ReadyReplicas}, err
}

func (s Service) StatusForTenant(ctx context.Context, tenantID, deploymentID string) (runtimev1.RuntimeStatus, error) {
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return runtimev1.RuntimeStatus{}, domain.NewError(domain.CodeInvalidArgument, "tenant id is required")
	}
	var value domain.Deployment
	err := s.Store.Transact(ctx, func(tx Tx) error {
		var ok bool
		value, ok = tx.GetDeployment(deploymentID)
		if !ok || value.TenantID != tenantID {
			return domain.NewError(domain.CodeNotFound, "deployment not found")
		}
		return nil
	})
	if err != nil {
		return runtimev1.RuntimeStatus{}, err
	}
	active := value.PreviousRelease
	if value.Phase == runtimev1.DeploymentReady {
		active = value.ReleaseID
	}
	return runtimev1.RuntimeStatus{DeploymentID: value.ID, Phase: value.Phase, ActiveRelease: active, URL: value.URL, ReadyReplicas: value.ReadyReplicas}, nil
}

func mustJSON(v any) []byte { raw, _ := json.Marshal(v); return raw }

func isRetryableGitRecovery(err error) bool {
	var typed *domain.Error
	return errors.As(err, &typed) && (typed.Code == domain.CodeUnavailable || typed.Code == domain.CodeStaleVersion)
}
