package kube

import (
	"context"
	"sync"

	runtimev1 "github.com/keir-research/ai-native-paas/pkg/contracts/runtime/v1"
)

type Metadata struct {
	Name, Namespace     string
	Labels, Annotations map[string]string
	OwnerReferences     []runtimev1.OwnerReference
	Generation          int64
}

type SecurityContext struct {
	RunAsNonRoot             bool
	AllowPrivilegeEscalation bool
	ReadOnlyRootFilesystem   bool
	SeccompProfile           string
	DropCapabilities         []string
}
type ResourceRequirements struct{ Requests, Limits runtimev1.ResourceQuantity }
type VolumeMount struct{ Name, MountPath string }
type Volume struct{ Name, EmptyDirSizeLimit string }
type Container struct {
	Name, Image                      string
	Command                          []string
	Port                             int
	HealthPath                       string
	StartupTimeout, ReadinessTimeout int
	SecurityContext                  SecurityContext
	Resources                        ResourceRequirements
	VolumeMounts                     []VolumeMount
}
type PodSpec struct {
	RuntimeClassName             string
	AutomountServiceAccountToken bool
	Containers                   []Container
	Volumes                      []Volume
}
type DeploymentSpec struct {
	Replicas                *int
	ProgressDeadlineSeconds int
	Pod                     PodSpec
}
type DeploymentStatus struct {
	ReadyReplicas int
	Failed        bool
	Message       string
}
type Deployment struct {
	Metadata Metadata
	Spec     DeploymentSpec
	Status   DeploymentStatus
}
type Service struct {
	Metadata Metadata
	Port     int
	Selector map[string]string
}
type HPA struct {
	Metadata                 Metadata
	TargetName               string
	MinReplicas, MaxReplicas int
}
type JobStatus string

const (
	JobPending   JobStatus = "Pending"
	JobSucceeded JobStatus = "Succeeded"
	JobFailed    JobStatus = "Failed"
)

type Job struct {
	Metadata       Metadata
	Image          string
	Command        []string
	TimeoutSeconds int
	Pod            PodSpec
	Status         JobStatus
	Message        string
}
type HTTPRoute struct {
	Metadata                 Metadata
	Hostname, BackendService string
	BackendPort              int
	TrafficEnabled           bool
}
type NetworkPolicy struct {
	Metadata                      Metadata
	DefaultDeny                   bool
	AllowedIngress, AllowedEgress []string
}

type Client interface {
	UpsertDeployment(context.Context, Deployment) error
	GetDeployment(context.Context, string, string) (Deployment, bool, error)
	ListDeployments(context.Context, string, map[string]string) ([]Deployment, error)
	DeleteDeployment(context.Context, string, string) error

	UpsertService(context.Context, Service) error
	GetService(context.Context, string, string) (Service, bool, error)
	ListServices(context.Context, string, map[string]string) ([]Service, error)
	DeleteService(context.Context, string, string) error

	UpsertHPA(context.Context, HPA) error
	ListHPAs(context.Context, string, map[string]string) ([]HPA, error)
	DeleteHPA(context.Context, string, string) error

	UpsertJob(context.Context, Job) error
	GetJob(context.Context, string, string) (Job, bool, error)
	ListJobs(context.Context, string, map[string]string) ([]Job, error)
	DeleteJob(context.Context, string, string) error

	UpsertHTTPRoute(context.Context, HTTPRoute) error
	GetHTTPRoute(context.Context, string, string) (HTTPRoute, bool, error)
	DeleteHTTPRoute(context.Context, string, string) error

	UpsertNetworkPolicy(context.Context, NetworkPolicy) error
	GetNetworkPolicy(context.Context, string, string) (NetworkPolicy, bool, error)
	DeleteNetworkPolicy(context.Context, string, string) error

	UpdatePaaSAppStatus(context.Context, string, string, runtimev1.PaaSAppStatus) error
}

func Key(namespace, name string) string { return namespace + "/" + name }
func LabelsMatch(actual, required map[string]string) bool {
	for k, v := range required {
		if actual[k] != v {
			return false
		}
	}
	return true
}
func CloneLabels(in map[string]string) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
func CloneMetadata(in Metadata) Metadata {
	in.Labels = CloneLabels(in.Labels)
	in.Annotations = CloneLabels(in.Annotations)
	in.OwnerReferences = append([]runtimev1.OwnerReference(nil), in.OwnerReferences...)
	return in
}

// OperationLog is intentionally tiny and deterministic; tests use it to prove
// route-before-workload deletion and reconciliation idempotency without a live API server.
type OperationLog struct {
	mu     sync.Mutex
	values []string
}

func (l *OperationLog) Add(v string) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.values = append(l.values, v)
}
func (l *OperationLog) Values() []string {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.values...)
}
