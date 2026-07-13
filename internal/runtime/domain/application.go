package domain

import (
	"strings"
	"time"

	runtimev1 "github.com/keir-research/ai-native-paas/pkg/contracts/runtime/v1"
)

type Application struct {
	ID        string
	TenantID  string
	ProjectID string
	Name      string
	Lifecycle runtimev1.LifecycleState
	Version   int64
	CreatedAt time.Time
	UpdatedAt time.Time
}

func NewApplication(id, tenantID, projectID, name string, now time.Time) (Application, error) {
	id, tenantID, projectID = strings.TrimSpace(id), strings.TrimSpace(tenantID), strings.TrimSpace(projectID)
	name = dnsSlug(name)
	if !runtimev1.ValidPlatformID(id) || !runtimev1.ValidPlatformID(tenantID) || !runtimev1.ValidPlatformID(projectID) || !runtimev1.ValidDNSLabel(name) {
		return Application{}, NewError(CodeInvalidArgument, "application identity is invalid")
	}
	now = now.UTC()
	return Application{ID: id, TenantID: tenantID, ProjectID: projectID, Name: name, Lifecycle: runtimev1.LifecycleActive, Version: 1, CreatedAt: now, UpdatedAt: now}, nil
}

func (a *Application) TransitionLifecycle(to runtimev1.LifecycleState, now time.Time) error {
	allowed := map[runtimev1.LifecycleState]map[runtimev1.LifecycleState]bool{
		runtimev1.LifecycleActive:          {runtimev1.LifecycleSuspending: true, runtimev1.LifecycleDeletingRuntime: true},
		runtimev1.LifecycleSuspending:      {runtimev1.LifecycleSuspended: true, runtimev1.LifecycleActive: true},
		runtimev1.LifecycleSuspended:       {runtimev1.LifecycleResuming: true, runtimev1.LifecycleDeletingRuntime: true},
		runtimev1.LifecycleResuming:        {runtimev1.LifecycleActive: true, runtimev1.LifecycleSuspended: true},
		runtimev1.LifecycleDeletingRuntime: {runtimev1.LifecycleRetaining: true, runtimev1.LifecycleActive: true},
		runtimev1.LifecycleRetaining:       {runtimev1.LifecycleDeleted: true},
	}
	if a.Lifecycle == to {
		return nil
	}
	if !allowed[a.Lifecycle][to] {
		return NewError(CodeConflict, "invalid application lifecycle transition")
	}
	a.Lifecycle = to
	a.Version++
	a.UpdatedAt = now.UTC()
	return nil
}
