// Package simulator defines the development-only runtime driver boundary.
//
// The simulator is intentionally not a Cozystack implementation. It exists to
// exercise product orchestration contracts inside a bounded admin-cluster
// namespace while real Cozystack remains a live release gate.
package simulator

import (
	"context"

	runtimev1 "github.com/keir-research/ai-native-paas/pkg/contracts/runtime/v1"
)

const (
	DriverName       = "runtime_sim_k8s"
	EvidenceClass    = "dev-product"
	Namespace        = "paas-runtime-sim"
	CellID           = "runtime-sim-k8s"
	Region           = "eu1"
	GitOpsRepository = "https://git.example.invalid/platform/runtime-sim.git"
	ClusterServer    = "https://kubernetes.default.svc"
	ArgoProject      = "runtime-sim-k8s"
	IngressDomain    = "sim.runtime.internal"
	CapacityUnits    = 1000
)

type WorkloadSpec struct {
	TenantID      string
	ProjectID     string
	ApplicationID string
	EnvironmentID string
	ReleaseID     string
	ImageDigest   string
	Hostname      string
	Processes     map[string]runtimev1.ProcessSpec
}

type ManagedResourceSpec struct {
	TenantID      string
	ProjectID     string
	EnvironmentID string
	Name          string
	Kind          ManagedResourceKind
	Plan          string
}

type ManagedResourceKind string

const (
	ManagedPostgres ManagedResourceKind = "sim_postgres"
	ManagedRedis    ManagedResourceKind = "sim_redis"
	ManagedS3       ManagedResourceKind = "sim_s3"
)

type WorkloadStatus struct {
	ReadyReplicas int
	URL           string
	Phase         runtimev1.DeploymentPhase
}

type ManagedResourceStatus struct {
	Ready      bool
	Endpoint   string
	ExternalID string
}

type RuntimeDriver interface {
	DeployWorkload(context.Context, WorkloadSpec) (WorkloadStatus, error)
	DeleteWorkload(context.Context, WorkloadSpec) error
	ProbeEndpoint(context.Context, string) error
	DiscoverWorkload(context.Context, WorkloadSpec) (WorkloadStatus, error)
	ReconcileDrift(context.Context, WorkloadSpec) (WorkloadStatus, error)
}

type ManagedResourceDriver interface {
	ApplyManagedResource(context.Context, ManagedResourceSpec) (ManagedResourceStatus, error)
	DeleteManagedResource(context.Context, ManagedResourceSpec) error
	DiscoverManagedResource(context.Context, ManagedResourceSpec) (ManagedResourceStatus, error)
}
