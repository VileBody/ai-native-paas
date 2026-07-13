package domain

import (
	"strings"
	"time"

	runtimev1 "github.com/keir-research/ai-native-paas/pkg/contracts/runtime/v1"
)

type Environment struct {
	ID              string
	TenantID        string
	ApplicationID   string
	Name            string
	Namespace       string
	Default         bool
	ActiveReleaseID string
	PlacementID     string
	Version         int64
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

func NewEnvironment(id, tenantID, applicationID, name string, isDefault bool, now time.Time) (Environment, error) {
	id, tenantID, applicationID = strings.TrimSpace(id), strings.TrimSpace(tenantID), strings.TrimSpace(applicationID)
	name = dnsSlug(name)
	if !runtimev1.ValidPlatformID(id) || !runtimev1.ValidPlatformID(tenantID) || !runtimev1.ValidPlatformID(applicationID) || !runtimev1.ValidDNSLabel(name) {
		return Environment{}, NewError(CodeInvalidArgument, "environment identity is invalid")
	}
	namespace := EnvironmentNamespace(applicationID, name)
	now = now.UTC()
	return Environment{ID: id, TenantID: tenantID, ApplicationID: applicationID, Name: name, Namespace: namespace, Default: isDefault, Version: 1, CreatedAt: now, UpdatedAt: now}, nil
}

func (e *Environment) SetPlacement(placementID string, now time.Time) error {
	placementID = strings.TrimSpace(placementID)
	if !runtimev1.ValidPlatformID(placementID) {
		return NewError(CodeInvalidArgument, "placement id is invalid")
	}
	if e.PlacementID == placementID {
		return nil
	}
	if e.PlacementID != "" {
		return NewError(CodeConflict, "placement is sticky and requires explicit migration")
	}
	e.PlacementID = placementID
	e.Version++
	e.UpdatedAt = now.UTC()
	return nil
}

func (e *Environment) ActivateRelease(releaseID string, now time.Time) error {
	releaseID = strings.TrimSpace(releaseID)
	if !runtimev1.ValidPlatformID(releaseID) {
		return NewError(CodeInvalidArgument, "release id is invalid")
	}
	if e.ActiveReleaseID == releaseID {
		return nil
	}
	e.ActiveReleaseID = releaseID
	e.Version++
	e.UpdatedAt = now.UTC()
	return nil
}
