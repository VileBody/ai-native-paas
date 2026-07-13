package operator

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/keir-research/ai-native-paas/internal/runtime/domain"
	"github.com/keir-research/ai-native-paas/internal/runtime/kube"
	runtimev1 "github.com/keir-research/ai-native-paas/pkg/contracts/runtime/v1"
)

type Clock interface{ Now() time.Time }

type Reconciler struct {
	Client kube.Client
	Clock  Clock
	Units  map[string]runtimev1.ResourceQuantity
}

func (r Reconciler) Reconcile(ctx context.Context, app runtimev1.PaaSApp) error {
	if r.Client == nil || r.Clock == nil {
		return domain.NewError(domain.CodeUnavailable, "operator dependencies are incomplete")
	}
	if err := app.Validate(); err != nil {
		return domain.Wrap(domain.CodeInvalidArgument, "reject invalid PaaSApp", err)
	}
	if app.Spec.Lifecycle.State == runtimev1.LifecycleDeletingRuntime || app.Spec.Lifecycle.State == runtimev1.LifecycleRetaining || app.Spec.Lifecycle.State == runtimev1.LifecycleDeleted {
		return r.reconcileDeletion(ctx, app)
	}
	if app.Spec.Lifecycle.State == runtimev1.LifecycleSuspending || app.Spec.Lifecycle.State == runtimev1.LifecycleSuspended {
		return r.reconcileSuspension(ctx, app)
	}
	resources, ok := r.Units[app.Spec.Runtime.Unit]
	if !ok {
		return domain.NewError(domain.CodeInvalidArgument, "unknown runtime unit")
	}
	labels := baseLabels(app)
	owner := ownerReference(app)
	policy := kube.NetworkPolicy{
		Metadata:       kube.Metadata{Name: "platform-default-deny", Namespace: app.Metadata.Namespace, Labels: labels, OwnerReferences: []runtimev1.OwnerReference{owner}},
		DefaultDeny:    true,
		AllowedIngress: []string{"platform-gateway"},
		AllowedEgress:  []string{app.Spec.Network.EgressProfile},
	}
	if err := r.Client.UpsertNetworkPolicy(ctx, policy); err != nil {
		return err
	}

	routeName := app.Metadata.Name
	route, routeExists, err := r.Client.GetHTTPRoute(ctx, app.Metadata.Namespace, routeName)
	if err != nil {
		return err
	}
	if !routeExists {
		route = kube.HTTPRoute{
			Metadata: kube.Metadata{Name: routeName, Namespace: app.Metadata.Namespace, Labels: labels, OwnerReferences: []runtimev1.OwnerReference{owner}},
			Hostname: app.Spec.Route.GeneratedHostname,
		}
	}
	route.Hostname = app.Spec.Route.GeneratedHostname
	status := app.Status
	status.ObservedGeneration = app.Metadata.Generation
	fingerprint := releaseSpecHash(app)
	if status.CandidateReleaseID == app.Spec.Identity.ReleaseID && status.CandidateSpecHash != "" && status.CandidateSpecHash != fingerprint {
		previousAvailable := true
		if status.ActiveReleaseID != "" {
			previousAvailable, err = r.routeToActive(ctx, app, &route, &status)
			if err != nil {
				return err
			}
		} else {
			route.BackendService = ""
			route.BackendPort = 0
			route.TrafficEnabled = false
		}
		status.Phase = "Degraded"
		reason := "ReleaseIdentityConflict"
		message := "PaaSApp spec changed without a new release identity"
		if !previousAvailable {
			reason = "PreviousReleaseUnavailable"
			message = "release identity conflict detected and the previous release service is unavailable"
		}
		status.Conditions = setCondition(status.Conditions, "Ready", "False", reason, message, app.Metadata.Generation, r.Clock.Now())
		if err := r.Client.UpsertHTTPRoute(ctx, route); err != nil {
			return err
		}
		return r.Client.UpdatePaaSAppStatus(ctx, app.Metadata.Namespace, app.Metadata.Name, status)
	}
	if status.CandidateReleaseID != app.Spec.Identity.ReleaseID {
		status.CandidateReleaseID = app.Spec.Identity.ReleaseID
		status.CandidateSpecHash = fingerprint
		status.StartedAt = r.Clock.Now()
	} else {
		if status.CandidateSpecHash == "" {
			status.CandidateSpecHash = fingerprint
		}
		if status.StartedAt.IsZero() {
			status.StartedAt = r.Clock.Now()
		}
	}
	migrationReady, migrationFailed, migrationMessage, err := r.ensureMigration(ctx, app, resources, labels, owner, &status)
	if err != nil {
		return err
	}

	// A release migration is a release-phase gate, not a sidecar to the
	// rollout. Candidate workloads are intentionally not created until the
	// migration has completed successfully; the previous release keeps
	// serving traffic during this phase.
	if !migrationReady {
		previousAvailable := true
		if status.ActiveReleaseID != "" && app.Spec.Lifecycle.State != runtimev1.LifecycleResuming {
			previousAvailable, err = r.routeToActive(ctx, app, &route, &status)
			if err != nil {
				return err
			}
		} else {
			route.BackendService = ""
			route.BackendPort = 0
			route.TrafficEnabled = false
		}
		switch {
		case !previousAvailable:
			status.Phase = "Degraded"
			status.Conditions = setCondition(status.Conditions, "Ready", "False", "PreviousReleaseUnavailable", "previous release service is unavailable while migration is pending", app.Metadata.Generation, r.Clock.Now())
		case migrationFailed:
			status.Phase = "Degraded"
			status.Conditions = setCondition(status.Conditions, "Ready", "False", "MigrationFailed", firstNonEmpty(migrationMessage, "release migration failed"), app.Metadata.Generation, r.Clock.Now())
		case timedOut(app, status, r.Clock.Now()):
			status.Phase = "Degraded"
			status.Conditions = setCondition(status.Conditions, "Ready", "False", "RolloutTimeout", "release migration did not complete before timeout", app.Metadata.Generation, r.Clock.Now())
		default:
			status.Phase = "Deploying"
			status.Conditions = setCondition(status.Conditions, "Ready", "False", "MigrationRunning", "release migration has not completed", app.Metadata.Generation, r.Clock.Now())
		}
		if err := r.Client.UpsertHTTPRoute(ctx, route); err != nil {
			return err
		}
		return r.Client.UpdatePaaSAppStatus(ctx, app.Metadata.Namespace, app.Metadata.Name, status)
	}

	ready := 0
	failed := false
	failureMessage := ""
	for _, name := range app.SortedProcessNames() {
		process := app.Spec.Processes[name]
		deployment := desiredDeployment(app, name, process, resources, labels, owner)
		if err := r.Client.UpsertDeployment(ctx, deployment); err != nil {
			return err
		}
		observed, _, err := r.Client.GetDeployment(ctx, app.Metadata.Namespace, deployment.Metadata.Name)
		if err != nil {
			return err
		}
		ready += observed.Status.ReadyReplicas
		if observed.Status.Failed {
			failed = true
			failureMessage = observed.Status.Message
		}
		if process.Port > 0 {
			service := kube.Service{
				Metadata: kube.Metadata{Name: serviceName(app.Metadata.Name, name, app.Spec.Identity.ReleaseID), Namespace: app.Metadata.Namespace, Labels: labels, OwnerReferences: []runtimev1.OwnerReference{owner}},
				Port:     process.Port,
				Selector: map[string]string{"platform.example.com/release-id": app.Spec.Identity.ReleaseID, "platform.example.com/process": name},
			}
			if err := r.Client.UpsertService(ctx, service); err != nil {
				return err
			}
		}
		if process.MaxReplicas > process.MinReplicas {
			hpa := kube.HPA{
				Metadata:    kube.Metadata{Name: deployment.Metadata.Name, Namespace: app.Metadata.Namespace, Labels: cloneLabels(deployment.Metadata.Labels), OwnerReferences: []runtimev1.OwnerReference{owner}},
				TargetName:  deployment.Metadata.Name,
				MinReplicas: process.MinReplicas,
				MaxReplicas: process.MaxReplicas,
			}
			if err := r.Client.UpsertHPA(ctx, hpa); err != nil {
				return err
			}
		} else if err := r.Client.DeleteHPA(ctx, app.Metadata.Namespace, deployment.Metadata.Name); err != nil {
			return err
		}
	}

	desiredBackend := serviceName(app.Metadata.Name, app.Spec.Route.Process, app.Spec.Identity.ReleaseID)
	wasAlreadyServingCurrent := app.Status.Phase == "Ready" && app.Status.ActiveReleaseID == app.Spec.Identity.ReleaseID && routeExists && route.TrafficEnabled && route.BackendService == desiredBackend
	allReady := !failed && allProcessesReady(ctx, r.Client, app)
	switch {
	case failed:
		status.Phase = "Degraded"
		status.Conditions = setCondition(status.Conditions, "Ready", "False", "RolloutFailed", firstNonEmpty(failureMessage, "candidate workload failed"), app.Metadata.Generation, r.Clock.Now())
	case allReady:
		route.BackendService = desiredBackend
		route.BackendPort = app.Spec.Processes[app.Spec.Route.Process].Port
		route.TrafficEnabled = true
		status.Phase = "Ready"
		status.ActiveReleaseID = app.Spec.Identity.ReleaseID
		status.ActiveBackendService = desiredBackend
		status.ActiveBackendPort = app.Spec.Processes[app.Spec.Route.Process].Port
		status.URL = "https://" + app.Spec.Route.GeneratedHostname
		status.ReadyReplicas = ready
		status.Conditions = setCondition(status.Conditions, "Ready", "True", "RolloutComplete", "candidate release is serving traffic", app.Metadata.Generation, r.Clock.Now())
	case timedOut(app, status, r.Clock.Now()):
		status.Phase = "Degraded"
		status.Conditions = setCondition(status.Conditions, "Ready", "False", "RolloutTimeout", "candidate release did not become ready before timeout", app.Metadata.Generation, r.Clock.Now())
	default:
		status.Phase = "Deploying"
		status.ReadyReplicas = ready
		status.Conditions = setCondition(status.Conditions, "Ready", "False", "Progressing", "candidate release is not ready", app.Metadata.Generation, r.Clock.Now())
	}
	if status.Phase != "Ready" {
		previousAvailable := true
		if status.ActiveReleaseID != "" && app.Spec.Lifecycle.State != runtimev1.LifecycleResuming {
			previousAvailable, err = r.routeToActive(ctx, app, &route, &status)
			if err != nil {
				return err
			}
		} else {
			route.BackendService = desiredBackend
			route.BackendPort = app.Spec.Processes[app.Spec.Route.Process].Port
			route.TrafficEnabled = false
		}
		if !previousAvailable {
			status.Phase = "Degraded"
			status.Conditions = setCondition(status.Conditions, "Ready", "False", "PreviousReleaseUnavailable", "previous release service is unavailable during candidate rollout", app.Metadata.Generation, r.Clock.Now())
		}
	}
	if err := r.Client.UpsertHTTPRoute(ctx, route); err != nil {
		return err
	}
	if err := r.Client.UpdatePaaSAppStatus(ctx, app.Metadata.Namespace, app.Metadata.Name, status); err != nil {
		return err
	}
	if wasAlreadyServingCurrent && status.Phase == "Ready" {
		return r.cleanupSuperseded(ctx, app)
	}
	return nil
}
func (r Reconciler) routeToActive(ctx context.Context, app runtimev1.PaaSApp, route *kube.HTTPRoute, status *runtimev1.PaaSAppStatus) (bool, error) {
	if status == nil || strings.TrimSpace(status.ActiveReleaseID) == "" {
		route.BackendService = ""
		route.BackendPort = 0
		route.TrafficEnabled = false
		return false, nil
	}
	backend := strings.TrimSpace(status.ActiveBackendService)
	port := status.ActiveBackendPort
	// Upgrade recovery: older operator versions did not persist the active
	// backend in status. Adopt the existing route only after verifying that its
	// Service is labeled for the active release and this application.
	if backend == "" && route.TrafficEnabled {
		backend = strings.TrimSpace(route.BackendService)
		port = route.BackendPort
	}
	if backend == "" || port <= 0 {
		route.BackendService = ""
		route.BackendPort = 0
		route.TrafficEnabled = false
		return false, nil
	}
	service, ok, err := r.Client.GetService(ctx, app.Metadata.Namespace, backend)
	if err != nil {
		return false, err
	}
	if !ok || service.Port != port || service.Metadata.Labels["platform.example.com/release-id"] != status.ActiveReleaseID || service.Metadata.Labels["platform.example.com/application-id"] != app.Spec.Identity.ApplicationID {
		route.BackendService = ""
		route.BackendPort = 0
		route.TrafficEnabled = false
		return false, nil
	}
	status.ActiveBackendService = backend
	status.ActiveBackendPort = port
	route.BackendService = backend
	route.BackendPort = port
	route.TrafficEnabled = true
	return true, nil
}

func (r Reconciler) ensureMigration(ctx context.Context, app runtimev1.PaaSApp, resources runtimev1.ResourceQuantity, labels map[string]string, owner runtimev1.OwnerReference, status *runtimev1.PaaSAppStatus) (bool, bool, string, error) {
	if len(app.Spec.Release.Migration.Command) == 0 {
		return true, false, "", nil
	}
	if status != nil && status.MigrationCompletedReleaseID == app.Spec.Identity.ReleaseID {
		return true, false, "", nil
	}
	name := migrationName(app.Metadata.Name, app.Spec.Identity.ReleaseID)
	job := kube.Job{
		Metadata:       kube.Metadata{Name: name, Namespace: app.Metadata.Namespace, Labels: labels, OwnerReferences: []runtimev1.OwnerReference{owner}},
		Image:          app.Spec.Image.Reference(),
		Command:        append([]string(nil), app.Spec.Release.Migration.Command...),
		TimeoutSeconds: app.Spec.Release.Migration.TimeoutSeconds,
		Pod:            securePodSpec(app.Spec.Runtime.Isolation, "migration", app.Spec.Image.Reference(), app.Spec.Release.Migration.Command, 0, "", 0, 0, resources),
		Status:         kube.JobPending,
	}
	if err := r.Client.UpsertJob(ctx, job); err != nil {
		return false, false, "", err
	}
	observed, _, err := r.Client.GetJob(ctx, app.Metadata.Namespace, name)
	if err != nil {
		return false, false, "", err
	}
	switch observed.Status {
	case kube.JobSucceeded:
		if status != nil {
			status.MigrationCompletedReleaseID = app.Spec.Identity.ReleaseID
		}
		return true, false, "", nil
	case kube.JobFailed:
		return false, true, observed.Message, nil
	default:
		return false, false, "", nil
	}
}

func desiredDeployment(app runtimev1.PaaSApp, processName string, process runtimev1.ProcessSpec, resources runtimev1.ResourceQuantity, labels map[string]string, owner runtimev1.OwnerReference) kube.Deployment {
	labels = cloneLabels(labels)
	labels["platform.example.com/process"] = processName
	labels["platform.example.com/release-id"] = app.Spec.Identity.ReleaseID
	var replicas *int
	if process.MaxReplicas <= process.MinReplicas {
		x := process.MinReplicas
		replicas = &x
	}
	return kube.Deployment{
		Metadata: kube.Metadata{Name: deploymentName(app.Metadata.Name, processName, app.Spec.Identity.ReleaseID), Namespace: app.Metadata.Namespace, Labels: labels, OwnerReferences: []runtimev1.OwnerReference{owner}},
		Spec: kube.DeploymentSpec{
			Replicas:                replicas,
			ProgressDeadlineSeconds: app.Spec.Release.RolloutTimeout,
			Pod: securePodSpec(
				app.Spec.Runtime.Isolation,
				processName,
				app.Spec.Image.Reference(),
				process.Command,
				process.Port,
				process.HealthPath,
				process.StartupTimeout,
				process.ReadinessTimeout,
				resources,
			),
		},
	}
}

func securePodSpec(isolation runtimev1.IsolationClass, containerName, image string, command []string, port int, healthPath string, startupTimeout, readinessTimeout int, resources runtimev1.ResourceQuantity) kube.PodSpec {
	runtimeClass := ""
	if isolation == runtimev1.IsolationSandboxed {
		runtimeClass = "gvisor"
	}
	return kube.PodSpec{
		RuntimeClassName:             runtimeClass,
		AutomountServiceAccountToken: false,
		Containers: []kube.Container{{
			Name:             containerName,
			Image:            image,
			Command:          append([]string(nil), command...),
			Port:             port,
			HealthPath:       healthPath,
			StartupTimeout:   startupTimeout,
			ReadinessTimeout: readinessTimeout,
			SecurityContext: kube.SecurityContext{
				RunAsNonRoot:             true,
				AllowPrivilegeEscalation: false,
				ReadOnlyRootFilesystem:   true,
				SeccompProfile:           "RuntimeDefault",
				DropCapabilities:         []string{"ALL"},
			},
			Resources:    kube.ResourceRequirements{Requests: resources, Limits: resources},
			VolumeMounts: []kube.VolumeMount{{Name: "tmp", MountPath: "/tmp"}},
		}},
		Volumes: []kube.Volume{{Name: "tmp", EmptyDirSizeLimit: resources.EphemeralStorage}},
	}
}

func (r Reconciler) reconcileDeletion(ctx context.Context, app runtimev1.PaaSApp) error {
	status := app.Status
	status.ObservedGeneration = app.Metadata.Generation
	if _, ok, err := r.Client.GetHTTPRoute(ctx, app.Metadata.Namespace, app.Metadata.Name); err != nil {
		return err
	} else if ok {
		if err := r.Client.DeleteHTTPRoute(ctx, app.Metadata.Namespace, app.Metadata.Name); err != nil {
			return err
		}
		status.Phase = "DeletingRoute"
		return r.Client.UpdatePaaSAppStatus(ctx, app.Metadata.Namespace, app.Metadata.Name, status)
	}
	remaining, err := r.deleteWorkloads(ctx, app, false)
	if err != nil {
		return err
	}
	if remaining {
		status.Phase = "DeletingWorkloads"
		status.Conditions = setCondition(status.Conditions, "Ready", "False", "RuntimeDeletionPending", "runtime workloads are terminating", app.Metadata.Generation, r.Clock.Now())
		return r.Client.UpdatePaaSAppStatus(ctx, app.Metadata.Namespace, app.Metadata.Name, status)
	}
	if _, ok, err := r.Client.GetNetworkPolicy(ctx, app.Metadata.Namespace, "platform-default-deny"); err != nil {
		return err
	} else if ok {
		if err := r.Client.DeleteNetworkPolicy(ctx, app.Metadata.Namespace, "platform-default-deny"); err != nil {
			return err
		}
		if _, stillExists, checkErr := r.Client.GetNetworkPolicy(ctx, app.Metadata.Namespace, "platform-default-deny"); checkErr != nil {
			return checkErr
		} else if stillExists {
			status.Phase = "DeletingPolicy"
			status.Conditions = setCondition(status.Conditions, "Ready", "False", "RuntimeDeletionPending", "runtime network policy is terminating", app.Metadata.Generation, r.Clock.Now())
			return r.Client.UpdatePaaSAppStatus(ctx, app.Metadata.Namespace, app.Metadata.Name, status)
		}
	}
	status.URL = ""
	status.ReadyReplicas = 0
	status.ActiveReleaseID = ""
	status.ActiveBackendService = ""
	status.ActiveBackendPort = 0
	status.CandidateReleaseID = ""
	status.MigrationCompletedReleaseID = ""
	status.CandidateSpecHash = ""
	status.StartedAt = time.Time{}
	if app.Spec.Lifecycle.State == runtimev1.LifecycleDeleted {
		status.Phase = "Deleted"
		status.Conditions = setCondition(status.Conditions, "Ready", "False", "RuntimePurged", "runtime resources are absent and the PaaSApp may be finalized", app.Metadata.Generation, r.Clock.Now())
	} else {
		status.Phase = "Retaining"
		status.Conditions = setCondition(status.Conditions, "Ready", "False", "RuntimeDeleted", "runtime resources were removed; external attachments are retained", app.Metadata.Generation, r.Clock.Now())
	}
	return r.Client.UpdatePaaSAppStatus(ctx, app.Metadata.Namespace, app.Metadata.Name, status)
}

func (r Reconciler) reconcileSuspension(ctx context.Context, app runtimev1.PaaSApp) error {
	status := app.Status
	status.ObservedGeneration = app.Metadata.Generation
	if _, ok, err := r.Client.GetHTTPRoute(ctx, app.Metadata.Namespace, app.Metadata.Name); err != nil {
		return err
	} else if ok {
		if err := r.Client.DeleteHTTPRoute(ctx, app.Metadata.Namespace, app.Metadata.Name); err != nil {
			return err
		}
		status.Phase = "Suspending"
		status.URL = ""
		status.Conditions = setCondition(status.Conditions, "Ready", "False", "TrafficSuspended", "application route was removed before compute suspension", app.Metadata.Generation, r.Clock.Now())
		return r.Client.UpdatePaaSAppStatus(ctx, app.Metadata.Namespace, app.Metadata.Name, status)
	}
	remaining, err := r.deleteWorkloads(ctx, app, false)
	if err != nil {
		return err
	}
	if remaining {
		status.Phase = "Suspending"
		status.Conditions = setCondition(status.Conditions, "Ready", "False", "ComputeSuspensionPending", "application workloads are terminating", app.Metadata.Generation, r.Clock.Now())
		return r.Client.UpdatePaaSAppStatus(ctx, app.Metadata.Namespace, app.Metadata.Name, status)
	}
	status.Phase = "Suspended"
	status.URL = ""
	status.ReadyReplicas = 0
	status.ActiveBackendService = ""
	status.ActiveBackendPort = 0
	status.CandidateReleaseID = ""
	status.CandidateSpecHash = ""
	status.StartedAt = time.Time{}
	status.Conditions = setCondition(status.Conditions, "Ready", "False", "Suspended", "application compute is suspended; external attachments are retained", app.Metadata.Generation, r.Clock.Now())
	return r.Client.UpdatePaaSAppStatus(ctx, app.Metadata.Namespace, app.Metadata.Name, status)
}

func (r Reconciler) cleanupSuperseded(ctx context.Context, app runtimev1.PaaSApp) error {
	_, err := r.deleteWorkloads(ctx, app, true)
	return err
}

// deleteWorkloads requests deletion and then performs a second read. Kubernetes
// deletion is asynchronous; callers only advance to Suspended/Retaining after
// the API no longer returns matching resources.
func (r Reconciler) deleteWorkloads(ctx context.Context, app runtimev1.PaaSApp, staleOnly bool) (bool, error) {
	labels := map[string]string{"platform.example.com/application-id": app.Spec.Identity.ApplicationID}
	current := app.Spec.Identity.ReleaseID
	stale := func(metadata kube.Metadata) bool {
		return !staleOnly || metadata.Labels["platform.example.com/release-id"] != current
	}
	deployments, err := r.Client.ListDeployments(ctx, app.Metadata.Namespace, labels)
	if err != nil {
		return false, err
	}
	for _, value := range deployments {
		if stale(value.Metadata) {
			if err := r.Client.DeleteDeployment(ctx, value.Metadata.Namespace, value.Metadata.Name); err != nil {
				return false, err
			}
		}
	}
	services, err := r.Client.ListServices(ctx, app.Metadata.Namespace, labels)
	if err != nil {
		return false, err
	}
	for _, value := range services {
		if stale(value.Metadata) {
			if err := r.Client.DeleteService(ctx, value.Metadata.Namespace, value.Metadata.Name); err != nil {
				return false, err
			}
		}
	}
	hpas, err := r.Client.ListHPAs(ctx, app.Metadata.Namespace, labels)
	if err != nil {
		return false, err
	}
	for _, value := range hpas {
		if stale(value.Metadata) {
			if err := r.Client.DeleteHPA(ctx, value.Metadata.Namespace, value.Metadata.Name); err != nil {
				return false, err
			}
		}
	}
	jobs, err := r.Client.ListJobs(ctx, app.Metadata.Namespace, labels)
	if err != nil {
		return false, err
	}
	for _, value := range jobs {
		if stale(value.Metadata) {
			if err := r.Client.DeleteJob(ctx, value.Metadata.Namespace, value.Metadata.Name); err != nil {
				return false, err
			}
		}
	}
	return r.matchingWorkloadsExist(ctx, app, staleOnly)
}

func (r Reconciler) matchingWorkloadsExist(ctx context.Context, app runtimev1.PaaSApp, staleOnly bool) (bool, error) {
	labels := map[string]string{"platform.example.com/application-id": app.Spec.Identity.ApplicationID}
	current := app.Spec.Identity.ReleaseID
	matches := func(metadata kube.Metadata) bool {
		return !staleOnly || metadata.Labels["platform.example.com/release-id"] != current
	}
	deployments, err := r.Client.ListDeployments(ctx, app.Metadata.Namespace, labels)
	if err != nil {
		return false, err
	}
	for _, value := range deployments {
		if matches(value.Metadata) {
			return true, nil
		}
	}
	services, err := r.Client.ListServices(ctx, app.Metadata.Namespace, labels)
	if err != nil {
		return false, err
	}
	for _, value := range services {
		if matches(value.Metadata) {
			return true, nil
		}
	}
	hpas, err := r.Client.ListHPAs(ctx, app.Metadata.Namespace, labels)
	if err != nil {
		return false, err
	}
	for _, value := range hpas {
		if matches(value.Metadata) {
			return true, nil
		}
	}
	jobs, err := r.Client.ListJobs(ctx, app.Metadata.Namespace, labels)
	if err != nil {
		return false, err
	}
	for _, value := range jobs {
		if matches(value.Metadata) {
			return true, nil
		}
	}
	return false, nil
}

func allProcessesReady(ctx context.Context, client kube.Client, app runtimev1.PaaSApp) bool {
	for _, name := range app.SortedProcessNames() {
		p := app.Spec.Processes[name]
		d, ok, err := client.GetDeployment(ctx, app.Metadata.Namespace, deploymentName(app.Metadata.Name, name, app.Spec.Identity.ReleaseID))
		if err != nil || !ok || d.Status.Failed || d.Status.ReadyReplicas < p.MinReplicas {
			return false
		}
	}
	return true
}
func releaseSpecHash(app runtimev1.PaaSApp) string {
	raw, _ := json.Marshal(struct {
		Identity              runtimev1.IdentitySpec           `json:"identity"`
		Image                 runtimev1.ImageSpec              `json:"image"`
		Runtime               runtimev1.RuntimeSpec            `json:"runtime"`
		Processes             map[string]runtimev1.ProcessSpec `json:"processes"`
		Release               runtimev1.ReleaseSpec            `json:"release"`
		Route                 runtimev1.RouteSpec              `json:"route"`
		AttachmentSnapshotRef string                           `json:"attachmentSnapshotRef"`
		Network               runtimev1.NetworkSpec            `json:"network"`
	}{app.Spec.Identity, app.Spec.Image, app.Spec.Runtime, app.Spec.Processes, app.Spec.Release, app.Spec.Route, app.Spec.AttachmentSnapshotRef, app.Spec.Network})
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func timedOut(app runtimev1.PaaSApp, status runtimev1.PaaSAppStatus, now time.Time) bool {
	return app.Spec.Release.RolloutTimeout > 0 && !status.StartedAt.IsZero() && now.Sub(status.StartedAt) > time.Duration(app.Spec.Release.RolloutTimeout)*time.Second
}
func baseLabels(app runtimev1.PaaSApp) map[string]string {
	return map[string]string{"platform.example.com/tenant-id": app.Spec.Identity.TenantID, "platform.example.com/application-id": app.Spec.Identity.ApplicationID, "platform.example.com/environment-id": app.Spec.Identity.EnvironmentID, "platform.example.com/release-id": app.Spec.Identity.ReleaseID, "platform.example.com/managed-by": "paas-operator"}
}
func cloneLabels(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
func ownerReference(app runtimev1.PaaSApp) runtimev1.OwnerReference {
	return runtimev1.OwnerReference{APIVersion: runtimev1.APIVersion, Kind: runtimev1.Kind, Name: app.Metadata.Name, UID: app.Metadata.UID, Controller: true, BlockOwnerDeletion: true}
}
func short(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:4])
}
func deploymentName(app, process, release string) string {
	return releaseObjectName(app, process, release)
}
func serviceName(app, process, release string) string {
	return releaseObjectName(app, process, release)
}
func migrationName(app, release string) string {
	return releaseObjectName(app, "migration", release)
}
func releaseObjectName(app, component, release string) string {
	base := truncateDNS(app+"-"+component, 54)
	if base == "" {
		base = "app"
	}
	// Hash the complete identity, not only the release. This preserves
	// uniqueness when long application/process names share a truncated prefix.
	suffix := "-" + short(app+"\x00"+component+"\x00"+release)
	base = truncateDNS(base, 63-len(suffix))
	return base + suffix
}
func truncateDNS(v string, n int) string {
	v = strings.ToLower(v)
	v = strings.Trim(v, "-")
	if len(v) <= n {
		return v
	}
	return strings.Trim(v[:n], "-")
}
func setCondition(existing []runtimev1.Condition, typ, status, reason, message string, generation int64, now time.Time) []runtimev1.Condition {
	out := append([]runtimev1.Condition(nil), existing...)
	for i := range out {
		if out[i].Type == typ {
			if out[i].Status == status && out[i].Reason == reason && out[i].Message == message {
				out[i].ObservedGeneration = generation
				return out
			}
			out[i] = runtimev1.Condition{Type: typ, Status: status, Reason: reason, Message: message, ObservedGeneration: generation, LastTransitionTime: now.UTC()}
			return out
		}
	}
	out = append(out, runtimev1.Condition{Type: typ, Status: status, Reason: reason, Message: message, ObservedGeneration: generation, LastTransitionTime: now.UTC()})
	sort.Slice(out, func(i, j int) bool { return out[i].Type < out[j].Type })
	return out
}
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return "rollout failed"
}
