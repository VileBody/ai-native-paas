package application

import (
	"context"
	"reflect"
	"strings"

	"github.com/keir-research/ai-native-paas/internal/runtime/domain"
	runtimev1 "github.com/keir-research/ai-native-paas/pkg/contracts/runtime/v1"
)

func (s Service) ReconcileDeploymentStatus(ctx context.Context, deploymentID string) error {
	if s.Observer == nil {
		return domain.NewError(domain.CodeUnavailable, "runtime observer is not configured")
	}
	var deployment domain.Deployment
	var env domain.Environment
	var placement domain.Placement
	var cell domain.RuntimeCell
	var app domain.Application
	var release domain.Release
	err := s.Store.Transact(ctx, func(tx Tx) error {
		var ok bool
		deployment, ok = tx.GetDeployment(deploymentID)
		if !ok {
			return domain.NewError(domain.CodeNotFound, "deployment not found")
		}
		env, ok = tx.GetEnvironment(deployment.EnvironmentID)
		if !ok {
			return domain.NewError(domain.CodeInternal, "environment missing")
		}
		placement, ok = tx.GetPlacement(deployment.PlacementID)
		if !ok {
			return domain.NewError(domain.CodeInternal, "placement missing")
		}
		cell, ok = tx.GetRuntimeCell(placement.CellID)
		if !ok {
			return domain.NewError(domain.CodeInternal, "cell missing")
		}
		app, ok = tx.GetApplication(deployment.ApplicationID)
		if !ok {
			return domain.NewError(domain.CodeInternal, "application missing")
		}
		release, ok = tx.GetRelease(deployment.ReleaseID)
		if !ok {
			return domain.NewError(domain.CodeInternal, "release missing")
		}
		return nil
	})
	if err != nil {
		return err
	}
	expectedName := domain.PaaSAppName(app.ID, env.Name)
	object, found, err := s.Observer.GetPaaSApp(ctx, RuntimeObjectRef{CellID: cell.ID, Namespace: env.Namespace, Name: expectedName})
	if err != nil {
		return err
	}
	if !found {
		return s.markArgoSyncing(ctx, deployment.ID)
	}
	if err := validateObservedPaaSApp(object, app, env, deployment, release, expectedName); err != nil {
		return err
	}
	// Kubernetes status is accepted only for the currently observed spec
	// generation. This prevents a stale Ready status from activating a newer
	// release after Argo has updated spec but the operator has not reconciled.
	if object.Metadata.Generation <= 0 || object.Status.ObservedGeneration < object.Metadata.Generation {
		return s.markRollingOut(ctx, deployment.ID)
	}
	return s.applyObservedStatus(ctx, deployment.ID, object.Status)
}

func validateObservedPaaSApp(object runtimev1.PaaSApp, app domain.Application, env domain.Environment, deployment domain.Deployment, release domain.Release, expectedName string) error {
	if object.APIVersion != runtimev1.APIVersion || object.Kind != runtimev1.Kind || object.Metadata.Namespace != env.Namespace || object.Metadata.Name != expectedName {
		return domain.NewError(domain.CodeConflict, "observed PaaSApp metadata does not match deployment")
	}
	identity := object.Spec.Identity
	if identity.TenantID != app.TenantID || identity.ProjectID != app.ProjectID || identity.ApplicationID != app.ID || identity.EnvironmentID != env.ID || identity.Environment != env.Name || identity.ReleaseID != deployment.ReleaseID {
		return domain.NewError(domain.CodeConflict, "observed PaaSApp identity does not match deployment")
	}
	expectedImage := runtimev1.ImageSpec{Repository: release.Artifact.Repository, Digest: release.Artifact.Digest, MediaType: release.Artifact.MediaType}
	if object.Spec.Image != expectedImage {
		return domain.NewError(domain.CodeConflict, "observed PaaSApp image does not match release artifact")
	}
	routeProcess, ok := runtimev1.SelectRouteProcess(release.Configuration.Processes)
	if !ok {
		return domain.NewError(domain.CodeInternal, "release has no route process")
	}
	expected := runtimev1.PaaSAppSpec{
		Identity:              identity,
		Image:                 expectedImage,
		Runtime:               runtimev1.RuntimeSpec{Isolation: release.Configuration.Isolation, Unit: release.Configuration.Unit},
		Processes:             cloneStatusProcesses(release.Configuration.Processes),
		Release:               runtimev1.ReleaseSpec{Strategy: "Rolling", Migration: release.Configuration.Migration, RolloutTimeout: release.Configuration.RolloutTimeoutSeconds, PreviousRelease: deployment.PreviousRelease},
		Route:                 runtimev1.RouteSpec{GeneratedHostname: release.Configuration.GeneratedHostname, Process: routeProcess},
		AttachmentSnapshotRef: release.Configuration.AttachmentSnapshotRef,
		Network:               runtimev1.NetworkSpec{EgressProfile: release.Configuration.EgressProfile},
		Lifecycle:             runtimev1.LifecycleSpec{State: app.Lifecycle},
	}
	// Preserve the expected identity value assembled from authoritative rows,
	// rather than trusting the one copied from the observed object.
	expected.Identity = runtimev1.IdentitySpec{TenantID: app.TenantID, ProjectID: app.ProjectID, ApplicationID: app.ID, EnvironmentID: env.ID, Environment: env.Name, ReleaseID: release.ID}
	if !reflect.DeepEqual(object.Spec, expected) {
		return domain.NewError(domain.CodeConflict, "observed PaaSApp spec does not match immutable release")
	}
	return nil
}

func cloneStatusProcesses(in map[string]runtimev1.ProcessSpec) map[string]runtimev1.ProcessSpec {
	out := make(map[string]runtimev1.ProcessSpec, len(in))
	for key, value := range in {
		value.Command = append([]string(nil), value.Command...)
		out[key] = value
	}
	return out
}

func (s Service) markArgoSyncing(ctx context.Context, deploymentID string) error {
	now := s.Clock.Now()
	return s.Store.Transact(ctx, func(tx Tx) error {
		d, ok := tx.GetDeployment(deploymentID)
		if !ok {
			return domain.NewError(domain.CodeNotFound, "deployment not found")
		}
		expected := d.Version
		if d.Phase == runtimev1.DeploymentGitCommitted {
			if err := d.Transition(runtimev1.DeploymentArgoSyncing, now); err != nil {
				return err
			}
			return tx.UpdateDeployment(d, expected)
		}
		return nil
	})
}

func (s Service) markRollingOut(ctx context.Context, deploymentID string) error {
	now := s.Clock.Now()
	return s.Store.Transact(ctx, func(tx Tx) error {
		deployment, ok := tx.GetDeployment(deploymentID)
		if !ok {
			return domain.NewError(domain.CodeNotFound, "deployment not found")
		}
		release, ok := tx.GetRelease(deployment.ReleaseID)
		if !ok {
			return domain.NewError(domain.CodeInternal, "release missing")
		}
		expectedDep, expectedRel := deployment.Version, release.Version
		switch deployment.Phase {
		case runtimev1.DeploymentGitCommitted:
			if err := deployment.Transition(runtimev1.DeploymentArgoSyncing, now); err != nil {
				return err
			}
			if err := deployment.Transition(runtimev1.DeploymentRollingOut, now); err != nil {
				return err
			}
		case runtimev1.DeploymentArgoSyncing, runtimev1.DeploymentDegraded:
			if err := deployment.Transition(runtimev1.DeploymentRollingOut, now); err != nil {
				return err
			}
		}
		if release.State == runtimev1.ReleaseCommittedToGitOps {
			if err := release.Transition(runtimev1.ReleaseDeploying, now); err != nil {
				return err
			}
		}
		if deployment.Version != expectedDep {
			if err := tx.UpdateDeployment(deployment, expectedDep); err != nil {
				return err
			}
		}
		if release.Version != expectedRel {
			if err := tx.UpdateRelease(release, expectedRel); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s Service) applyObservedStatus(ctx context.Context, deploymentID string, status runtimev1.PaaSAppStatus) error {
	now := s.Clock.Now()
	return s.Store.Transact(ctx, func(tx Tx) error {
		deployment, ok := tx.GetDeployment(deploymentID)
		if !ok {
			return domain.NewError(domain.CodeNotFound, "deployment not found")
		}
		release, ok := tx.GetRelease(deployment.ReleaseID)
		if !ok {
			return domain.NewError(domain.CodeInternal, "release missing")
		}
		env, ok := tx.GetEnvironment(deployment.EnvironmentID)
		if !ok {
			return domain.NewError(domain.CodeInternal, "environment missing")
		}
		expectedDep, expectedRel, expectedEnv := deployment.Version, release.Version, env.Version
		if deployment.Phase == runtimev1.DeploymentGitCommitted {
			if err := deployment.Transition(runtimev1.DeploymentArgoSyncing, now); err != nil {
				return err
			}
		}
		if deployment.Phase == runtimev1.DeploymentArgoSyncing {
			if err := deployment.Transition(runtimev1.DeploymentRollingOut, now); err != nil {
				return err
			}
		}
		if release.State == runtimev1.ReleaseCommittedToGitOps {
			if err := release.Transition(runtimev1.ReleaseDeploying, now); err != nil {
				return err
			}
		}
		switch strings.ToLower(status.Phase) {
		case "ready":
			if status.ActiveReleaseID != release.ID {
				return domain.NewError(domain.CodeConflict, "PaaSApp reports another active release")
			}
			for _, other := range tx.ListReleasesByEnvironment(env.ID) {
				if other.ID != release.ID && other.State == runtimev1.ReleaseActive {
					expected := other.Version
					if err := other.Transition(runtimev1.ReleaseSuperseded, now); err != nil {
						return err
					}
					if err := tx.UpdateRelease(other, expected); err != nil {
						return err
					}
				}
			}
			if release.State == runtimev1.ReleaseDeploying {
				if err := release.Transition(runtimev1.ReleaseActive, now); err != nil {
					return err
				}
			}
			if err := deployment.MarkReady(status.URL, status.ReadyReplicas, now); err != nil {
				return err
			}
			if err := env.ActivateRelease(release.ID, now); err != nil {
				return err
			}
		case "degraded":
			if deployment.Phase != runtimev1.DeploymentDegraded {
				if err := deployment.MarkDegraded("ROLLOUT_DEGRADED", conditionMessage(status.Conditions), now); err != nil {
					return err
				}
			}
			// A degraded observation is recoverable. The immutable candidate
			// remains DEPLOYING and may later become Ready without a new release.
		default:
			if deployment.Phase == runtimev1.DeploymentDegraded {
				if err := deployment.Transition(runtimev1.DeploymentRollingOut, now); err != nil {
					return err
				}
			}
		}
		if deployment.Version != expectedDep {
			if err := tx.UpdateDeployment(deployment, expectedDep); err != nil {
				return err
			}
		}
		if release.Version != expectedRel {
			if err := tx.UpdateRelease(release, expectedRel); err != nil {
				return err
			}
		}
		if env.Version != expectedEnv {
			if err := tx.UpdateEnvironment(env, expectedEnv); err != nil {
				return err
			}
		}
		return nil
	})
}

func conditionMessage(conditions []runtimev1.Condition) string {
	for _, condition := range conditions {
		if condition.Type == "Ready" && condition.Message != "" {
			return condition.Message
		}
	}
	return "runtime rollout is degraded"
}

type ObservedObject struct {
	CellID, Namespace, Kind, Name string
	Labels                        map[string]string
}

func (s Service) ObserveRuntimeObject(ctx context.Context, object ObservedObject) error {
	releaseID := object.Labels["platform.example.com/release-id"]
	known := false
	if releaseID != "" {
		_ = s.Store.Transact(ctx, func(tx Tx) error {
			deployment, ok := tx.FindDeploymentByRelease(releaseID)
			if !ok {
				return nil
			}
			placement, ok := tx.GetPlacement(deployment.PlacementID)
			if !ok {
				return nil
			}
			env, ok := tx.GetEnvironment(deployment.EnvironmentID)
			if !ok {
				return nil
			}
			app, ok := tx.GetApplication(deployment.ApplicationID)
			if !ok {
				return nil
			}
			known = object.Kind == runtimev1.Kind && object.CellID == placement.CellID && object.Namespace == env.Namespace && object.Name == domain.PaaSAppName(app.ID, env.Name)
			return nil
		})
	}
	if known {
		return nil
	}
	return s.Store.Transact(ctx, func(tx Tx) error {
		return tx.InsertQuarantine(domain.QuarantineRecord{ID: s.IDs.New("qua"), CellID: object.CellID, Namespace: object.Namespace, Kind: object.Kind, Name: object.Name, Reason: "object is not owned by a known runtime deployment", ObservedAt: s.Clock.Now()})
	})
}
