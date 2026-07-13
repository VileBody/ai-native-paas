package domain

import (
	"strings"
	"time"

	runtimev1 "github.com/keir-research/ai-native-paas/pkg/contracts/runtime/v1"
)

type Placement struct {
	ID            string
	TenantID      string
	EnvironmentID string
	CellID        string
	Region        string
	Isolation     runtimev1.IsolationClass
	Current       bool
	Units         int
	Version       int64
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

func NewPlacement(id, tenantID, environmentID string, cell RuntimeCell, isolation runtimev1.IsolationClass, units int, now time.Time) (Placement, error) {
	id, tenantID, environmentID = strings.TrimSpace(id), strings.TrimSpace(tenantID), strings.TrimSpace(environmentID)
	if !runtimev1.ValidPlatformID(id) || !runtimev1.ValidPlatformID(tenantID) || !runtimev1.ValidPlatformID(environmentID) || !cell.CanPlace(cell.Region, isolation, units) {
		return Placement{}, NewError(CodeInvalidArgument, "placement is invalid or incompatible")
	}
	now = now.UTC()
	return Placement{ID: id, TenantID: tenantID, EnvironmentID: environmentID, CellID: cell.ID, Region: cell.Region, Isolation: isolation, Current: true, Units: units, Version: 1, CreatedAt: now, UpdatedAt: now}, nil
}
func (p *Placement) Resize(units int, now time.Time) error {
	if units <= 0 {
		return NewError(CodeInvalidArgument, "placement units must be positive")
	}
	if p.Units == units {
		return nil
	}
	p.Units = units
	p.Version++
	p.UpdatedAt = now.UTC()
	return nil
}
func (p *Placement) Retire(now time.Time) error {
	if !p.Current {
		return nil
	}
	p.Current = false
	p.Version++
	p.UpdatedAt = now.UTC()
	return nil
}
