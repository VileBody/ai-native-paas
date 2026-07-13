package domain

import (
	"strings"
	"time"

	buildv1 "github.com/keir-research/ai-native-paas/pkg/contracts/build/v1"
	runtimev1 "github.com/keir-research/ai-native-paas/pkg/contracts/runtime/v1"
)

type Release struct {
	ID             string
	TenantID       string
	ApplicationID  string
	EnvironmentID  string
	Identity       string
	Artifact       buildv1.ArtifactRef
	Configuration  runtimev1.ReleaseConfig
	PolicyVersion  string
	State          runtimev1.ReleaseState
	RollbackOf     string
	PolicyOverride bool
	FailureCode    string
	FailureMessage string
	Version        int64
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

func NewRelease(id, tenantID, applicationID, environmentID string, artifact buildv1.ArtifactRef, config runtimev1.ReleaseConfig, policyVersion string, now time.Time) (Release, error) {
	id, tenantID, applicationID, environmentID, policyVersion = strings.TrimSpace(id), strings.TrimSpace(tenantID), strings.TrimSpace(applicationID), strings.TrimSpace(environmentID), strings.TrimSpace(policyVersion)
	if !runtimev1.ValidPlatformID(id) || !runtimev1.ValidPlatformID(tenantID) || !runtimev1.ValidPlatformID(applicationID) || !runtimev1.ValidPlatformID(environmentID) || artifact.Validate() != nil || policyVersion == "" {
		return Release{}, NewError(CodeInvalidArgument, "release identity is invalid")
	}
	normalized, err := NormalizeReleaseConfig(config)
	if err != nil {
		return Release{}, err
	}
	identity, err := ReleaseIdentity(environmentID, artifact, normalized)
	if err != nil {
		return Release{}, err
	}
	now = now.UTC()
	return Release{ID: id, TenantID: tenantID, ApplicationID: applicationID, EnvironmentID: environmentID, Identity: identity, Artifact: artifact, Configuration: normalized, PolicyVersion: policyVersion, State: runtimev1.ReleaseCreated, Version: 1, CreatedAt: now, UpdatedAt: now}, nil
}

func (r *Release) Transition(to runtimev1.ReleaseState, now time.Time) error {
	if r.State == to {
		return nil
	}
	allowed := map[runtimev1.ReleaseState]map[runtimev1.ReleaseState]bool{
		runtimev1.ReleaseCreated:           {runtimev1.ReleaseValidated: true},
		runtimev1.ReleaseValidated:         {runtimev1.ReleaseCommittedToGitOps: true, runtimev1.ReleaseFailed: true},
		runtimev1.ReleaseCommittedToGitOps: {runtimev1.ReleaseDeploying: true, runtimev1.ReleaseFailed: true},
		runtimev1.ReleaseDeploying:         {runtimev1.ReleaseActive: true, runtimev1.ReleaseFailed: true},
		runtimev1.ReleaseActive:            {runtimev1.ReleaseSuperseded: true},
	}
	if !allowed[r.State][to] {
		return NewError(CodeConflict, "invalid release transition")
	}
	r.State = to
	r.Version++
	r.UpdatedAt = now.UTC()
	return nil
}
func (r *Release) Fail(code, message string, now time.Time) error {
	if r.State == runtimev1.ReleaseFailed {
		return nil
	}
	if err := r.Transition(runtimev1.ReleaseFailed, now); err != nil {
		return err
	}
	r.FailureCode, r.FailureMessage = strings.TrimSpace(code), strings.TrimSpace(message)
	return nil
}
