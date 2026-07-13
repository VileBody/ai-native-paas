package domain

import (
	"sort"
	"strings"
	"time"

	runtimev1 "github.com/keir-research/ai-native-paas/pkg/contracts/runtime/v1"
)

type CellState string

const (
	CellActive   CellState = "ACTIVE"
	CellDraining CellState = "DRAINING"
	CellOffline  CellState = "OFFLINE"
)

type RuntimeCell struct {
	ID               string
	Region           string
	Isolation        []runtimev1.IsolationClass
	State            CellState
	CapacityUnits    int
	AllocatedUnits   int
	GitOpsRepository string
	GitOpsRevision   string
	ClusterServer    string
	ArgoProject      string
	IngressDomain    string
	Version          int64
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

func NewRuntimeCell(id, region string, isolation []runtimev1.IsolationClass, capacity int, gitopsRepository, clusterServer, argoProject, ingressDomain string, now time.Time) (RuntimeCell, error) {
	id = strings.ToLower(strings.TrimSpace(id))
	region = strings.ToLower(strings.TrimSpace(region))
	gitopsRepository, clusterServer, argoProject = strings.TrimSpace(gitopsRepository), strings.TrimSpace(clusterServer), strings.TrimSpace(argoProject)
	ingressDomain = strings.ToLower(strings.TrimSpace(strings.TrimSuffix(ingressDomain, ".")))
	if !runtimev1.ValidDNSLabel(id) || !runtimev1.ValidDNSLabel(region) || capacity <= 0 || gitopsRepository == "" || clusterServer == "" || !runtimev1.ValidDNSLabel(argoProject) || !runtimev1.ValidDNSSubdomain(ingressDomain) {
		return RuntimeCell{}, NewError(CodeInvalidArgument, "runtime cell configuration is invalid")
	}
	seen := map[runtimev1.IsolationClass]bool{}
	out := make([]runtimev1.IsolationClass, 0, len(isolation))
	for _, value := range isolation {
		if value != runtimev1.IsolationSandboxed && value != runtimev1.IsolationDedicated {
			return RuntimeCell{}, NewError(CodeInvalidArgument, "runtime cell isolation is invalid")
		}
		if !seen[value] {
			seen[value] = true
			out = append(out, value)
		}
	}
	if len(out) == 0 {
		return RuntimeCell{}, NewError(CodeInvalidArgument, "runtime cell supports no isolation class")
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	now = now.UTC()
	return RuntimeCell{ID: id, Region: region, Isolation: out, State: CellActive, CapacityUnits: capacity, GitOpsRepository: gitopsRepository, GitOpsRevision: "main", ClusterServer: clusterServer, ArgoProject: argoProject, IngressDomain: ingressDomain, Version: 1, CreatedAt: now, UpdatedAt: now}, nil
}

func (c RuntimeCell) supports(value runtimev1.IsolationClass) bool {
	for _, item := range c.Isolation {
		if item == value {
			return true
		}
	}
	return false
}
func (c RuntimeCell) CanPlace(region string, isolation runtimev1.IsolationClass, units int) bool {
	return c.State == CellActive && c.Region == strings.ToLower(strings.TrimSpace(region)) && c.supports(isolation) && units > 0 && c.AllocatedUnits <= c.CapacityUnits-units
}
func (c *RuntimeCell) Allocate(units int, now time.Time) error {
	if c.State != CellActive || units <= 0 || c.AllocatedUnits > c.CapacityUnits-units {
		return NewError(CodeCapacity, "runtime cell has insufficient capacity")
	}
	c.AllocatedUnits += units
	c.Version++
	c.UpdatedAt = now.UTC()
	return nil
}
func (c *RuntimeCell) Release(units int, now time.Time) error {
	if units <= 0 || units > c.AllocatedUnits {
		return NewError(CodeInvalidArgument, "runtime cell release is invalid")
	}
	c.AllocatedUnits -= units
	c.Version++
	c.UpdatedAt = now.UTC()
	return nil
}
func (c *RuntimeCell) Drain(now time.Time) error {
	if c.State == CellDraining {
		return nil
	}
	if c.State != CellActive {
		return NewError(CodeConflict, "runtime cell cannot enter draining state")
	}
	c.State = CellDraining
	c.Version++
	c.UpdatedAt = now.UTC()
	return nil
}
func (c RuntimeCell) SameConfiguration(other RuntimeCell) bool {
	if c.ID != other.ID || c.Region != other.Region || c.CapacityUnits != other.CapacityUnits || c.GitOpsRepository != other.GitOpsRepository || c.GitOpsRevision != other.GitOpsRevision || c.ClusterServer != other.ClusterServer || c.ArgoProject != other.ArgoProject || c.IngressDomain != other.IngressDomain || len(c.Isolation) != len(other.Isolation) {
		return false
	}
	for i := range c.Isolation {
		if c.Isolation[i] != other.Isolation[i] {
			return false
		}
	}
	return true
}
