package domain

import (
	"strings"
	"time"

	runtimev1 "github.com/keir-research/ai-native-paas/pkg/contracts/runtime/v1"
)

type Deployment struct {
	ID              string
	TenantID        string
	ApplicationID   string
	EnvironmentID   string
	ReleaseID       string
	PlacementID     string
	PreviousRelease string
	Phase           runtimev1.DeploymentPhase
	GitCommitSHA    string
	URL             string
	ReadyReplicas   int
	FailureCode     string
	FailureMessage  string
	Version         int64
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

func NewDeployment(id string, release Release, placement Placement, previousRelease string, now time.Time) (Deployment, error) {
	id = strings.TrimSpace(id)
	if !runtimev1.ValidPlatformID(id) || release.ID == "" || placement.ID == "" || release.TenantID != placement.TenantID || release.EnvironmentID != placement.EnvironmentID {
		return Deployment{}, NewError(CodeInvalidArgument, "deployment identity is invalid")
	}
	if previousRelease != "" && !runtimev1.ValidPlatformID(previousRelease) {
		return Deployment{}, NewError(CodeInvalidArgument, "previous release id is invalid")
	}
	now = now.UTC()
	return Deployment{ID: id, TenantID: release.TenantID, ApplicationID: release.ApplicationID, EnvironmentID: release.EnvironmentID, ReleaseID: release.ID, PlacementID: placement.ID, PreviousRelease: previousRelease, Phase: runtimev1.DeploymentPending, Version: 1, CreatedAt: now, UpdatedAt: now}, nil
}

func (d *Deployment) Transition(to runtimev1.DeploymentPhase, now time.Time) error {
	if d.Phase == to {
		return nil
	}
	allowed := map[runtimev1.DeploymentPhase]map[runtimev1.DeploymentPhase]bool{
		runtimev1.DeploymentPending:      {runtimev1.DeploymentGitCommitted: true, runtimev1.DeploymentFailed: true},
		runtimev1.DeploymentGitCommitted: {runtimev1.DeploymentArgoSyncing: true, runtimev1.DeploymentFailed: true},
		runtimev1.DeploymentArgoSyncing:  {runtimev1.DeploymentRollingOut: true, runtimev1.DeploymentDegraded: true, runtimev1.DeploymentFailed: true},
		runtimev1.DeploymentRollingOut:   {runtimev1.DeploymentReady: true, runtimev1.DeploymentDegraded: true, runtimev1.DeploymentFailed: true},
		runtimev1.DeploymentDegraded:     {runtimev1.DeploymentRollingOut: true, runtimev1.DeploymentReady: true, runtimev1.DeploymentFailed: true},
	}
	if !allowed[d.Phase][to] {
		return NewError(CodeConflict, "invalid deployment transition")
	}
	d.Phase = to
	d.Version++
	d.UpdatedAt = now.UTC()
	return nil
}
func (d *Deployment) RecordGitCommit(sha string, now time.Time) error {
	sha = strings.TrimSpace(sha)
	if len(sha) < 7 || len(sha) > 64 || strings.ContainsAny(sha, "\r\n\x00 ") {
		return NewError(CodeInvalidArgument, "git commit sha is invalid")
	}
	if d.GitCommitSHA != "" {
		if d.GitCommitSHA == sha {
			return nil
		}
		return NewError(CodeConflict, "deployment Git commit is immutable")
	}
	if err := d.Transition(runtimev1.DeploymentGitCommitted, now); err != nil {
		return err
	}
	d.GitCommitSHA = sha
	return nil
}
func (d *Deployment) MarkReady(url string, replicas int, now time.Time) error {
	if replicas < 0 || strings.TrimSpace(url) == "" {
		return NewError(CodeInvalidArgument, "ready deployment status is invalid")
	}
	if d.Phase != runtimev1.DeploymentReady {
		if err := d.Transition(runtimev1.DeploymentReady, now); err != nil {
			return err
		}
	}
	d.URL, d.ReadyReplicas, d.FailureCode, d.FailureMessage = strings.TrimSpace(url), replicas, "", ""
	return nil
}
func (d *Deployment) MarkDegraded(code, message string, now time.Time) error {
	if d.Phase != runtimev1.DeploymentDegraded {
		if err := d.Transition(runtimev1.DeploymentDegraded, now); err != nil {
			return err
		}
	}
	d.FailureCode, d.FailureMessage = strings.TrimSpace(code), strings.TrimSpace(message)
	return nil
}
