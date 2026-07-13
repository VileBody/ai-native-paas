package v1

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	buildv1 "github.com/keir-research/ai-native-paas/pkg/contracts/build/v1"
)

const (
	APIVersion = "platform.example.com/v1alpha1"
	Kind       = "PaaSApp"
)

var (
	ErrInvalidPaaSApp       = errors.New("invalid PaaSApp")
	ErrInvalidDeployRequest = errors.New("invalid deploy request")
)

type IsolationClass string

const (
	IsolationSandboxed IsolationClass = "sandboxed"
	IsolationDedicated IsolationClass = "dedicated"
)

type LifecycleState string

const (
	LifecycleActive          LifecycleState = "Active"
	LifecycleSuspending      LifecycleState = "Suspending"
	LifecycleSuspended       LifecycleState = "Suspended"
	LifecycleResuming        LifecycleState = "Resuming"
	LifecycleDeletingRuntime LifecycleState = "DeletingRuntime"
	LifecycleRetaining       LifecycleState = "Retaining"
	LifecycleDeleted         LifecycleState = "Deleted"
)

type ReleaseState string

const (
	ReleaseCreated           ReleaseState = "CREATED"
	ReleaseValidated         ReleaseState = "VALIDATED"
	ReleaseCommittedToGitOps ReleaseState = "COMMITTED_TO_GITOPS"
	ReleaseDeploying         ReleaseState = "DEPLOYING"
	ReleaseActive            ReleaseState = "ACTIVE"
	ReleaseFailed            ReleaseState = "FAILED"
	ReleaseSuperseded        ReleaseState = "SUPERSEDED"
)

type DeploymentPhase string

const (
	DeploymentPending      DeploymentPhase = "PENDING"
	DeploymentGitCommitted DeploymentPhase = "GIT_COMMITTED"
	DeploymentArgoSyncing  DeploymentPhase = "ARGO_SYNCING"
	DeploymentRollingOut   DeploymentPhase = "ROLLING_OUT"
	DeploymentReady        DeploymentPhase = "READY"
	DeploymentDegraded     DeploymentPhase = "DEGRADED"
	DeploymentFailed       DeploymentPhase = "FAILED"
)

type TypeMeta struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
}

type OwnerReference struct {
	APIVersion         string `json:"apiVersion"`
	Kind               string `json:"kind"`
	Name               string `json:"name"`
	UID                string `json:"uid,omitempty"`
	Controller         bool   `json:"controller"`
	BlockOwnerDeletion bool   `json:"blockOwnerDeletion"`
}

type ObjectMeta struct {
	Name            string            `json:"name"`
	Namespace       string            `json:"namespace,omitempty"`
	UID             string            `json:"uid,omitempty"`
	Generation      int64             `json:"generation,omitempty"`
	ResourceVersion string            `json:"resourceVersion,omitempty"`
	Labels          map[string]string `json:"labels,omitempty"`
	Annotations     map[string]string `json:"annotations,omitempty"`
	OwnerReferences []OwnerReference  `json:"ownerReferences,omitempty"`
}

type IdentitySpec struct {
	TenantID      string `json:"tenantID"`
	ProjectID     string `json:"projectID"`
	ApplicationID string `json:"applicationID"`
	EnvironmentID string `json:"environmentID"`
	Environment   string `json:"environment"`
	ReleaseID     string `json:"releaseID"`
}

type ImageSpec struct {
	Repository string `json:"repository"`
	Digest     string `json:"digest"`
	MediaType  string `json:"mediaType"`
}

func (i ImageSpec) Reference() string { return i.Repository + "@" + i.Digest }

type RuntimeSpec struct {
	Isolation IsolationClass `json:"isolation"`
	Unit      string         `json:"unit"`
}

type ResourceQuantity struct {
	CPU              string `json:"cpu"`
	Memory           string `json:"memory"`
	EphemeralStorage string `json:"ephemeralStorage"`
}

type ProcessSpec struct {
	Command          []string `json:"command,omitempty"`
	Port             int      `json:"port,omitempty"`
	MinReplicas      int      `json:"minReplicas"`
	MaxReplicas      int      `json:"maxReplicas"`
	HealthPath       string   `json:"healthPath,omitempty"`
	StartupTimeout   int      `json:"startupTimeoutSeconds,omitempty"`
	ReadinessTimeout int      `json:"readinessTimeoutSeconds,omitempty"`
}

type MigrationSpec struct {
	Command        []string `json:"command,omitempty"`
	TimeoutSeconds int      `json:"timeoutSeconds,omitempty"`
}

type ReleaseSpec struct {
	Strategy        string        `json:"strategy"`
	Migration       MigrationSpec `json:"migration,omitempty"`
	RolloutTimeout  int           `json:"rolloutTimeoutSeconds"`
	PreviousRelease string        `json:"previousReleaseID,omitempty"`
}

type RouteSpec struct {
	GeneratedHostname string `json:"generatedHostname"`
	Process           string `json:"process"`
}

type NetworkSpec struct {
	EgressProfile string   `json:"egressProfile"`
	Peers         []string `json:"peers,omitempty"`
}

type LifecycleSpec struct {
	State LifecycleState `json:"state"`
}

type PaaSAppSpec struct {
	Identity              IdentitySpec           `json:"identity"`
	Image                 ImageSpec              `json:"image"`
	Runtime               RuntimeSpec            `json:"runtime"`
	Processes             map[string]ProcessSpec `json:"processes"`
	Release               ReleaseSpec            `json:"release"`
	Route                 RouteSpec              `json:"route"`
	AttachmentSnapshotRef string                 `json:"attachmentSnapshotRef"`
	Network               NetworkSpec            `json:"network"`
	Lifecycle             LifecycleSpec          `json:"lifecycle"`
}

type Condition struct {
	Type               string    `json:"type"`
	Status             string    `json:"status"`
	Reason             string    `json:"reason,omitempty"`
	Message            string    `json:"message,omitempty"`
	ObservedGeneration int64     `json:"observedGeneration,omitempty"`
	LastTransitionTime time.Time `json:"lastTransitionTime,omitempty"`
}

type PaaSAppStatus struct {
	ObservedGeneration          int64       `json:"observedGeneration,omitempty"`
	Phase                       string      `json:"phase,omitempty"`
	ActiveReleaseID             string      `json:"activeReleaseID,omitempty"`
	ActiveBackendService        string      `json:"activeBackendService,omitempty"`
	ActiveBackendPort           int         `json:"activeBackendPort,omitempty"`
	CandidateReleaseID          string      `json:"candidateReleaseID,omitempty"`
	CandidateSpecHash           string      `json:"candidateSpecHash,omitempty"`
	MigrationCompletedReleaseID string      `json:"migrationCompletedReleaseID,omitempty"`
	URL                         string      `json:"url,omitempty"`
	ReadyReplicas               int         `json:"readyReplicas,omitempty"`
	StartedAt                   time.Time   `json:"startedAt,omitempty"`
	Conditions                  []Condition `json:"conditions,omitempty"`
}

type PaaSApp struct {
	TypeMeta `json:",inline"`
	Metadata ObjectMeta    `json:"metadata"`
	Spec     PaaSAppSpec   `json:"spec"`
	Status   PaaSAppStatus `json:"status,omitempty"`
}

func (a PaaSApp) Validate() error {
	if a.APIVersion != APIVersion || a.Kind != Kind {
		return fmt.Errorf("%w: unsupported apiVersion or kind", ErrInvalidPaaSApp)
	}
	if !ValidDNSLabel(a.Metadata.Name) || !ValidDNSLabel(a.Metadata.Namespace) {
		return fmt.Errorf("%w: metadata name and namespace are required", ErrInvalidPaaSApp)
	}
	identity := a.Spec.Identity
	if !ValidPlatformID(identity.TenantID) || !ValidPlatformID(identity.ProjectID) ||
		!ValidPlatformID(identity.ApplicationID) || !ValidPlatformID(identity.EnvironmentID) ||
		!ValidDNSLabel(identity.Environment) || !ValidPlatformID(identity.ReleaseID) {
		return fmt.Errorf("%w: identity fields must be platform-safe identifiers", ErrInvalidPaaSApp)
	}
	artifact := buildv1.ArtifactRef{ArtifactID: identity.ReleaseID, Repository: a.Spec.Image.Repository, Digest: a.Spec.Image.Digest, MediaType: a.Spec.Image.MediaType}
	if !ValidOCIRepository(artifact.Repository) || !buildv1.ValidDigest(artifact.Digest) || !validScalar(artifact.MediaType, 255) {
		return fmt.Errorf("%w: exact image repository, digest and media type are required", ErrInvalidPaaSApp)
	}
	if a.Spec.Runtime.Isolation != IsolationSandboxed && a.Spec.Runtime.Isolation != IsolationDedicated {
		return fmt.Errorf("%w: unsupported isolation class", ErrInvalidPaaSApp)
	}
	if !ValidDNSLabel(a.Spec.Runtime.Unit) || len(a.Spec.Processes) == 0 || len(a.Spec.Processes) > 32 {
		return fmt.Errorf("%w: runtime unit and at least one process are required", ErrInvalidPaaSApp)
	}
	for name, process := range a.Spec.Processes {
		if !ValidDNSLabel(name) || process.MinReplicas < 0 || process.MaxReplicas < process.MinReplicas || process.MaxReplicas > 1000 || (process.MaxReplicas > process.MinReplicas && process.MinReplicas == 0) {
			return fmt.Errorf("%w: invalid process %q", ErrInvalidPaaSApp, name)
		}
		if !validCommand(process.Command) || process.Port < 0 || process.Port > 65535 {
			return fmt.Errorf("%w: invalid process command or port", ErrInvalidPaaSApp)
		}
		if process.HealthPath != "" && (len(process.HealthPath) > 2048 || !strings.HasPrefix(process.HealthPath, "/") || strings.ContainsAny(process.HealthPath, "\r\n\x00")) {
			return fmt.Errorf("%w: invalid health path", ErrInvalidPaaSApp)
		}
		if process.StartupTimeout < 0 || process.StartupTimeout > 3600 || process.ReadinessTimeout < 0 || process.ReadinessTimeout > 3600 {
			return fmt.Errorf("%w: invalid process timeout", ErrInvalidPaaSApp)
		}
	}
	routeProcess, ok := a.Spec.Processes[a.Spec.Route.Process]
	if !ok {
		return fmt.Errorf("%w: route process is not declared", ErrInvalidPaaSApp)
	}
	if routeProcess.Port <= 0 || routeProcess.MinReplicas < 1 {
		return fmt.Errorf("%w: route process must expose a port and at least one replica", ErrInvalidPaaSApp)
	}
	if !ValidDNSSubdomain(a.Spec.Route.GeneratedHostname) {
		return fmt.Errorf("%w: generated hostname is required", ErrInvalidPaaSApp)
	}
	if a.Spec.Release.Strategy != "Rolling" || a.Spec.Release.RolloutTimeout <= 0 || a.Spec.Release.RolloutTimeout > 86400 {
		return fmt.Errorf("%w: unsupported release strategy or timeout", ErrInvalidPaaSApp)
	}
	if len(a.Spec.Release.Migration.Command) > 0 && (!validCommand(a.Spec.Release.Migration.Command) || a.Spec.Release.Migration.TimeoutSeconds <= 0 || a.Spec.Release.Migration.TimeoutSeconds > 86400) {
		return fmt.Errorf("%w: migration command and timeout are invalid", ErrInvalidPaaSApp)
	}
	if !validScalar(a.Spec.AttachmentSnapshotRef, 255) {
		return fmt.Errorf("%w: attachment snapshot reference is required", ErrInvalidPaaSApp)
	}
	if a.Spec.Network.EgressProfile != "public-default" && a.Spec.Network.EgressProfile != "deny-all" {
		return fmt.Errorf("%w: unsupported egress profile", ErrInvalidPaaSApp)
	}
	if len(a.Spec.Network.Peers) > 0 {
		return fmt.Errorf("%w: network peers are not supported in v1alpha1", ErrInvalidPaaSApp)
	}
	switch a.Spec.Lifecycle.State {
	case LifecycleActive, LifecycleSuspending, LifecycleSuspended, LifecycleResuming, LifecycleDeletingRuntime, LifecycleRetaining, LifecycleDeleted:
	default:
		return fmt.Errorf("%w: unsupported lifecycle state", ErrInvalidPaaSApp)
	}
	return nil
}

// ValidPlatformID is the bounded label-value subset used by platform-owned
// aggregate identifiers. These identifiers are copied into Kubernetes labels
// and GitOps metadata, so accepting arbitrary control/path characters here
// would make reconciliation fail after partial side effects.
func ValidPlatformID(value string) bool {
	if len(value) < 1 || len(value) > 63 {
		return false
	}
	for i, ch := range value {
		alphaNumeric := (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9')
		if (i == 0 || i == len(value)-1) && !alphaNumeric {
			return false
		}
		if !alphaNumeric && ch != '-' && ch != '_' && ch != '.' {
			return false
		}
	}
	return true
}

// ValidOCIRepository accepts a registry/repository name without a tag or
// digest. Runtime always combines this value with an independently validated
// sha256 digest.
func ValidOCIRepository(value string) bool {
	if value != strings.ToLower(strings.TrimSpace(value)) || len(value) < 3 || len(value) > 255 || strings.ContainsAny(value, "@\r\n\t ") || strings.Contains(value, "://") || strings.HasPrefix(value, "/") || strings.HasSuffix(value, "/") {
		return false
	}
	slash := strings.LastIndex(value, "/")
	if slash < 1 || strings.Contains(value[slash+1:], ":") {
		return false
	}
	parts := strings.Split(value, "/")
	for index, part := range parts {
		if part == "" || part == "." || part == ".." {
			return false
		}
		if index == 0 {
			host := part
			if colon := strings.LastIndex(host, ":"); colon >= 0 {
				if colon == len(host)-1 {
					return false
				}
				for _, ch := range host[colon+1:] {
					if ch < '0' || ch > '9' {
						return false
					}
				}
				host = host[:colon]
			}
			if host == "" || strings.HasPrefix(host, ".") || strings.HasSuffix(host, ".") {
				return false
			}
			for _, ch := range host {
				if (ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9') || ch == '.' || ch == '-' {
					continue
				}
				return false
			}
			continue
		}
		if part[0] == '.' || part[0] == '-' || part[len(part)-1] == '.' || part[len(part)-1] == '-' {
			return false
		}
		for _, ch := range part {
			if (ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9') || ch == '.' || ch == '_' || ch == '-' {
				continue
			}
			return false
		}
	}
	return true
}

func validScalar(value string, max int) bool {
	value = strings.TrimSpace(value)
	return value != "" && len(value) <= max && !strings.ContainsAny(value, "\r\n\x00")
}

func validCommand(values []string) bool {
	if len(values) > 64 {
		return false
	}
	for _, value := range values {
		if !validScalar(value, 4096) {
			return false
		}
	}
	return true
}

// ValidDNSLabel implements the Kubernetes/DNS label subset used for runtime
// object names and process identifiers. It intentionally accepts lowercase
// ASCII only so rendering is byte-stable across clients and locales.
func ValidDNSLabel(value string) bool {
	if len(value) < 1 || len(value) > 63 || value[0] == '-' || value[len(value)-1] == '-' {
		return false
	}
	for _, ch := range value {
		if (ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9') || ch == '-' {
			continue
		}
		return false
	}
	return true
}

// ValidDNSSubdomain validates generated platform hostnames. Custom-domain
// ownership is deliberately outside this iteration.
func ValidDNSSubdomain(value string) bool {
	if len(value) < 1 || len(value) > 253 || value != strings.ToLower(value) || strings.HasSuffix(value, ".") {
		return false
	}
	parts := strings.Split(value, ".")
	for _, part := range parts {
		if !ValidDNSLabel(part) {
			return false
		}
	}
	return true
}

// SelectRouteProcess applies the platform convention used before Iteration 5
// introduces richer routing configuration. A healthy "web" process wins;
// otherwise the lexicographically first process with a port and at least one
// replica is selected. Background-only releases are not routable.
func SelectRouteProcess(processes map[string]ProcessSpec) (string, bool) {
	if web, ok := processes["web"]; ok && web.Port > 0 && web.MinReplicas >= 1 {
		return "web", true
	}
	names := make([]string, 0, len(processes))
	for name, process := range processes {
		if process.Port > 0 && process.MinReplicas >= 1 {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return "", false
	}
	sort.Strings(names)
	return names[0], true
}

func (a PaaSApp) SortedProcessNames() []string {
	out := make([]string, 0, len(a.Spec.Processes))
	for name := range a.Spec.Processes {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

type DeployRequest struct {
	TenantID       string              `json:"tenant_id"`
	ApplicationID  string              `json:"application_id"`
	EnvironmentID  string              `json:"environment_id"`
	Artifact       buildv1.ArtifactRef `json:"artifact"`
	Configuration  ReleaseConfig       `json:"configuration"`
	IdempotencyKey string              `json:"idempotency_key"`
	ActorID        string              `json:"actor_id"`
}

type ReleaseConfig struct {
	Region                string                 `json:"region"`
	Isolation             IsolationClass         `json:"isolation"`
	Unit                  string                 `json:"unit"`
	Processes             map[string]ProcessSpec `json:"processes"`
	GeneratedHostname     string                 `json:"generated_hostname"`
	AttachmentSnapshotRef string                 `json:"attachment_snapshot_ref"`
	Migration             MigrationSpec          `json:"migration"`
	RolloutTimeoutSeconds int                    `json:"rollout_timeout_seconds"`
	EgressProfile         string                 `json:"egress_profile"`
}

func (r DeployRequest) Validate() error {
	if strings.TrimSpace(r.TenantID) == "" || strings.TrimSpace(r.ApplicationID) == "" ||
		strings.TrimSpace(r.EnvironmentID) == "" || strings.TrimSpace(r.ActorID) == "" || strings.TrimSpace(r.IdempotencyKey) == "" {
		return fmt.Errorf("%w: identity and idempotency fields are required", ErrInvalidDeployRequest)
	}
	if err := r.Artifact.Validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidDeployRequest, err)
	}
	if strings.TrimSpace(r.Configuration.Region) == "" || strings.TrimSpace(r.Configuration.Unit) == "" || len(r.Configuration.Processes) == 0 {
		return fmt.Errorf("%w: release configuration is incomplete", ErrInvalidDeployRequest)
	}
	return nil
}

type DeploymentRef struct {
	DeploymentID string          `json:"deployment_id"`
	ReleaseID    string          `json:"release_id"`
	Phase        DeploymentPhase `json:"phase"`
}

type RuntimeStatus struct {
	DeploymentID  string          `json:"deployment_id"`
	Phase         DeploymentPhase `json:"phase"`
	ActiveRelease string          `json:"active_release"`
	URL           string          `json:"url"`
	ReadyReplicas int             `json:"ready_replicas"`
}

type DeploymentService interface {
	Deploy(context.Context, DeployRequest) (DeploymentRef, error)
	Rollback(context.Context, string, string) (DeploymentRef, error)
	Status(context.Context, string) (RuntimeStatus, error)
}
