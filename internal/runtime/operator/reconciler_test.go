package operator

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/keir-research/ai-native-paas/internal/runtime/kube"
	"github.com/keir-research/ai-native-paas/internal/runtime/testkit"
	runtimev1 "github.com/keir-research/ai-native-paas/pkg/contracts/runtime/v1"
)

type operatorFixture struct {
	ctx        context.Context
	client     *kube.MemoryClient
	clock      *testkit.Clock
	reconciler Reconciler
	app        runtimev1.PaaSApp
}

func newOperatorFixture(t *testing.T) *operatorFixture {
	t.Helper()
	clock := &testkit.Clock{T: time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)}
	client := kube.NewMemoryClient()
	app := runtimev1.PaaSApp{
		TypeMeta: runtimev1.TypeMeta{APIVersion: runtimev1.APIVersion, Kind: runtimev1.Kind},
		Metadata: runtimev1.ObjectMeta{Name: "booking-production", Namespace: "app-booking-production", UID: "uid-1", Generation: 1},
		Spec: runtimev1.PaaSAppSpec{
			Identity: runtimev1.IdentitySpec{TenantID: "tenant-1", ProjectID: "project-1", ApplicationID: "app-1", EnvironmentID: "env-1", Environment: "production", ReleaseID: "rel-1"},
			Image:    runtimev1.ImageSpec{Repository: "registry.test/tenants/tenant-1/apps/app-1", Digest: "sha256:" + strings.Repeat("a", 64), MediaType: "application/vnd.oci.image.manifest.v1+json"},
			Runtime:  runtimev1.RuntimeSpec{Isolation: runtimev1.IsolationSandboxed, Unit: "u1"},
			Processes: map[string]runtimev1.ProcessSpec{
				"web":    {Port: 8080, MinReplicas: 1, MaxReplicas: 3, HealthPath: "/health", StartupTimeout: 60, ReadinessTimeout: 30},
				"worker": {Command: []string{"./worker"}, MinReplicas: 1, MaxReplicas: 1},
			},
			Release:               runtimev1.ReleaseSpec{Strategy: "Rolling", RolloutTimeout: 300},
			Route:                 runtimev1.RouteSpec{GeneratedHostname: "booking-production.apps.eu1.test", Process: "web"},
			AttachmentSnapshotRef: "none",
			Network:               runtimev1.NetworkSpec{EgressProfile: "public-default"},
			Lifecycle:             runtimev1.LifecycleSpec{State: runtimev1.LifecycleActive},
		},
	}
	if err := app.Validate(); err != nil {
		t.Fatal(err)
	}
	client.PutPaaSApp(app)
	return &operatorFixture{
		ctx: context.Background(), client: client, clock: clock, app: app,
		reconciler: Reconciler{Client: client, Clock: clock, Units: map[string]runtimev1.ResourceQuantity{"u1": {CPU: "250m", Memory: "512Mi", EphemeralStorage: "1Gi"}}},
	}
}

func (f *operatorFixture) reconcile(t *testing.T) runtimev1.PaaSApp {
	t.Helper()
	if err := f.reconciler.Reconcile(f.ctx, f.app); err != nil {
		t.Fatal(err)
	}
	observed, ok := f.client.GetPaaSAppObject(f.app.Metadata.Namespace, f.app.Metadata.Name)
	if !ok {
		t.Fatal("PaaSApp missing from fake client")
	}
	f.app = observed
	return observed
}
func (f *operatorFixture) deployment(process string) kube.Deployment {
	return f.client.Deployments[kube.Key(f.app.Metadata.Namespace, deploymentName(f.app.Metadata.Name, process, f.app.Spec.Identity.ReleaseID))]
}
func (f *operatorFixture) route() kube.HTTPRoute {
	return f.client.Routes[kube.Key(f.app.Metadata.Namespace, f.app.Metadata.Name)]
}
func (f *operatorFixture) markAllReady() {
	for name, process := range f.app.Spec.Processes {
		f.client.SetDeploymentStatus(f.app.Metadata.Namespace, deploymentName(f.app.Metadata.Name, name, f.app.Spec.Identity.ReleaseID), kube.DeploymentStatus{ReadyReplicas: process.MinReplicas})
	}
}
func ownerIsPaaSApp(t *testing.T, refs []runtimev1.OwnerReference, app runtimev1.PaaSApp) {
	t.Helper()
	if len(refs) != 1 {
		t.Fatalf("owner refs=%v", refs)
	}
	ref := refs[0]
	if ref.APIVersion != runtimev1.APIVersion || ref.Kind != runtimev1.Kind || ref.Name != app.Metadata.Name || ref.UID != app.Metadata.UID || !ref.Controller || !ref.BlockOwnerDeletion {
		t.Fatalf("owner ref=%+v", ref)
	}
}

func TestOperator_CreatesDeploymentServiceAndHTTPRoute(t *testing.T) {
	f := newOperatorFixture(t)
	f.reconcile(t)
	if len(f.client.Deployments) != 2 || len(f.client.Services) != 1 || len(f.client.Routes) != 1 {
		t.Fatalf("deployments=%d services=%d routes=%d", len(f.client.Deployments), len(f.client.Services), len(f.client.Routes))
	}
	route := f.route()
	if route.Hostname != f.app.Spec.Route.GeneratedHostname || route.TrafficEnabled {
		t.Fatalf("route=%+v", route)
	}
}

func TestOperator_SetsOwnerReferencesOnChildren(t *testing.T) {
	f := newOperatorFixture(t)
	f.reconcile(t)
	for _, value := range f.client.Deployments {
		ownerIsPaaSApp(t, value.Metadata.OwnerReferences, f.app)
	}
	for _, value := range f.client.Services {
		ownerIsPaaSApp(t, value.Metadata.OwnerReferences, f.app)
	}
	for _, value := range f.client.HPAs {
		ownerIsPaaSApp(t, value.Metadata.OwnerReferences, f.app)
	}
	for _, value := range f.client.Routes {
		ownerIsPaaSApp(t, value.Metadata.OwnerReferences, f.app)
	}
	for _, value := range f.client.Policies {
		ownerIsPaaSApp(t, value.Metadata.OwnerReferences, f.app)
	}
}

func TestOperator_SetsRuntimeClassGVisorForSandboxedTier(t *testing.T) {
	f := newOperatorFixture(t)
	f.reconcile(t)
	for _, value := range f.client.Deployments {
		if value.Spec.Pod.RuntimeClassName != "gvisor" {
			t.Fatalf("runtimeClassName=%q", value.Spec.Pod.RuntimeClassName)
		}
	}
}

func TestOperator_SetsRestrictedSecurityContext(t *testing.T) {
	f := newOperatorFixture(t)
	f.reconcile(t)
	for _, value := range f.client.Deployments {
		for _, container := range value.Spec.Pod.Containers {
			sc := container.SecurityContext
			if !sc.RunAsNonRoot || sc.AllowPrivilegeEscalation || !sc.ReadOnlyRootFilesystem || sc.SeccompProfile != "RuntimeDefault" || !reflect.DeepEqual(sc.DropCapabilities, []string{"ALL"}) {
				t.Fatalf("securityContext=%+v", sc)
			}
		}
	}
}

func TestOperator_DisablesServiceAccountToken(t *testing.T) {
	f := newOperatorFixture(t)
	f.reconcile(t)
	for _, value := range f.client.Deployments {
		if value.Spec.Pod.AutomountServiceAccountToken {
			t.Fatal("service account token enabled")
		}
	}
}

func TestOperator_SetsRequestsLimitsAndEphemeralStorage(t *testing.T) {
	f := newOperatorFixture(t)
	f.reconcile(t)
	want := f.reconciler.Units["u1"]
	for _, value := range f.client.Deployments {
		for _, container := range value.Spec.Pod.Containers {
			if container.Resources.Requests != want || container.Resources.Limits != want {
				t.Fatalf("resources=%+v", container.Resources)
			}
			if len(value.Spec.Pod.Volumes) != 1 || value.Spec.Pod.Volumes[0].EmptyDirSizeLimit != want.EphemeralStorage {
				t.Fatalf("volumes=%+v", value.Spec.Pod.Volumes)
			}
		}
	}
}

func TestOperator_CreatesDefaultDenyNetworkPolicy(t *testing.T) {
	f := newOperatorFixture(t)
	f.reconcile(t)
	policy, ok := f.client.Policies[kube.Key(f.app.Metadata.Namespace, "platform-default-deny")]
	if !ok || !policy.DefaultDeny || !reflect.DeepEqual(policy.AllowedIngress, []string{"platform-gateway"}) || !reflect.DeepEqual(policy.AllowedEgress, []string{"public-default"}) {
		t.Fatalf("policy=%+v", policy)
	}
}

func TestOperator_ReconcileIsIdempotent(t *testing.T) {
	f := newOperatorFixture(t)
	first := f.reconcile(t)
	deployments := cloneDeploymentMap(f.client.Deployments)
	services := cloneServiceMap(f.client.Services)
	hpas := cloneHPAMap(f.client.HPAs)
	routes := cloneRouteMap(f.client.Routes)
	policies := clonePolicyMap(f.client.Policies)
	second := f.reconcile(t)
	if !reflect.DeepEqual(deployments, f.client.Deployments) || !reflect.DeepEqual(services, f.client.Services) || !reflect.DeepEqual(hpas, f.client.HPAs) || !reflect.DeepEqual(routes, f.client.Routes) || !reflect.DeepEqual(policies, f.client.Policies) || !reflect.DeepEqual(first.Status, second.Status) {
		t.Fatal("second reconciliation changed desired resources or status")
	}
}

func TestOperator_UpdatesStatusObservedGeneration(t *testing.T) {
	f := newOperatorFixture(t)
	observed := f.reconcile(t)
	if observed.Status.ObservedGeneration != observed.Metadata.Generation {
		t.Fatalf("status=%+v metadata=%+v", observed.Status, observed.Metadata)
	}
}

func TestRollout_ReadinessSuccessActivatesRelease(t *testing.T) {
	f := newOperatorFixture(t)
	f.reconcile(t)
	f.markAllReady()
	observed := f.reconcile(t)
	if observed.Status.Phase != "Ready" || observed.Status.ActiveReleaseID != observed.Spec.Identity.ReleaseID || observed.Status.ReadyReplicas != 2 {
		t.Fatalf("status=%+v", observed.Status)
	}
	route := f.route()
	if !route.TrafficEnabled || route.BackendService != serviceName(f.app.Metadata.Name, "web", f.app.Spec.Identity.ReleaseID) {
		t.Fatalf("route=%+v", route)
	}
}

func TestRollout_ReadinessFailureKeepsPreviousReleaseActive(t *testing.T) {
	f := newOperatorFixture(t)
	oldRelease := "rel-old"
	oldService := "booking-production-web-old"
	labels := map[string]string{"platform.example.com/application-id": f.app.Spec.Identity.ApplicationID, "platform.example.com/release-id": oldRelease}
	if err := f.client.UpsertService(f.ctx, kube.Service{Metadata: kube.Metadata{Name: oldService, Namespace: f.app.Metadata.Namespace, Labels: labels}, Port: 8080}); err != nil {
		t.Fatal(err)
	}
	if err := f.client.UpsertHTTPRoute(f.ctx, kube.HTTPRoute{Metadata: kube.Metadata{Name: f.app.Metadata.Name, Namespace: f.app.Metadata.Namespace}, Hostname: f.app.Spec.Route.GeneratedHostname, BackendService: oldService, BackendPort: 8080, TrafficEnabled: true}); err != nil {
		t.Fatal(err)
	}
	f.app.Status = runtimev1.PaaSAppStatus{Phase: "Ready", ActiveReleaseID: oldRelease, ActiveBackendService: oldService, ActiveBackendPort: 8080}
	f.client.PutPaaSApp(f.app)
	f.reconcile(t)
	f.client.SetDeploymentStatus(f.app.Metadata.Namespace, deploymentName(f.app.Metadata.Name, "web", f.app.Spec.Identity.ReleaseID), kube.DeploymentStatus{Failed: true, Message: "crash loop"})
	observed := f.reconcile(t)
	if observed.Status.Phase != "Degraded" || observed.Status.ActiveReleaseID != oldRelease {
		t.Fatalf("status=%+v", observed.Status)
	}
	route := f.route()
	if !route.TrafficEnabled || route.BackendService != oldService {
		t.Fatalf("previous route not preserved: %+v", route)
	}
}

func TestRollout_MigrationRunsOncePerRelease(t *testing.T) {
	f := newOperatorFixture(t)
	f.app.Spec.Release.Migration = runtimev1.MigrationSpec{Command: []string{"./app", "migrate"}, TimeoutSeconds: 600}
	f.client.PutPaaSApp(f.app)
	first := f.reconcile(t)
	if len(f.client.Jobs) != 1 || len(f.client.Deployments) != 0 || first.Status.Phase != "Deploying" {
		t.Fatalf("jobs=%d deployments=%d status=%+v", len(f.client.Jobs), len(f.client.Deployments), first.Status)
	}
	name := migrationName(f.app.Metadata.Name, f.app.Spec.Identity.ReleaseID)
	f.client.SetJobStatus(f.app.Metadata.Namespace, name, kube.JobSucceeded, "")
	second := f.reconcile(t)
	if second.Status.MigrationCompletedReleaseID != f.app.Spec.Identity.ReleaseID || len(f.client.Deployments) != 2 || len(f.client.Jobs) != 1 {
		t.Fatalf("status=%+v jobs=%d deployments=%d", second.Status, len(f.client.Jobs), len(f.client.Deployments))
	}
}

func TestRollout_MigrationFailureBlocksTrafficSwitch(t *testing.T) {
	f := newOperatorFixture(t)
	f.app.Spec.Release.Migration = runtimev1.MigrationSpec{Command: []string{"./app", "migrate"}, TimeoutSeconds: 600}
	f.client.PutPaaSApp(f.app)
	f.reconcile(t)
	name := migrationName(f.app.Metadata.Name, f.app.Spec.Identity.ReleaseID)
	f.client.SetJobStatus(f.app.Metadata.Namespace, name, kube.JobFailed, "migration exploded")
	observed := f.reconcile(t)
	if observed.Status.Phase != "Degraded" || len(f.client.Deployments) != 0 || f.route().TrafficEnabled {
		t.Fatalf("status=%+v deployments=%d route=%+v", observed.Status, len(f.client.Deployments), f.route())
	}
}

func TestRollout_TimeoutMarksDeploymentDegraded(t *testing.T) {
	f := newOperatorFixture(t)
	f.app.Spec.Release.RolloutTimeout = 10
	f.app.Status = runtimev1.PaaSAppStatus{CandidateReleaseID: f.app.Spec.Identity.ReleaseID, CandidateSpecHash: releaseSpecHash(f.app), StartedAt: f.clock.Now().Add(-11 * time.Second)}
	f.client.PutPaaSApp(f.app)
	observed := f.reconcile(t)
	if observed.Status.Phase != "Degraded" || conditionReason(observed.Status.Conditions, "Ready") != "RolloutTimeout" {
		t.Fatalf("status=%+v", observed.Status)
	}
}

func TestRollout_RetryDoesNotRepeatSuccessfulMigration(t *testing.T) {
	f := newOperatorFixture(t)
	f.app.Spec.Release.Migration = runtimev1.MigrationSpec{Command: []string{"./app", "migrate"}, TimeoutSeconds: 600}
	f.client.PutPaaSApp(f.app)
	f.reconcile(t)
	name := migrationName(f.app.Metadata.Name, f.app.Spec.Identity.ReleaseID)
	f.client.SetJobStatus(f.app.Metadata.Namespace, name, kube.JobSucceeded, "")
	f.reconcile(t)
	before := countLog(f.client.Log.Values(), "upsert job ")
	f.reconcile(t)
	after := countLog(f.client.Log.Values(), "upsert job ")
	if before != 2 || after != before {
		t.Fatalf("migration job was repeated: before=%d after=%d log=%v", before, after, f.client.Log.Values())
	}
}

func TestOperator_HPAOwnsReplicaCountWhenAutoscalingEnabled(t *testing.T) {
	f := newOperatorFixture(t)
	f.reconcile(t)
	web := f.deployment("web")
	if web.Spec.Replicas != nil {
		t.Fatalf("operator set replicas while HPA owns scale: %v", *web.Spec.Replicas)
	}
	hpa, ok := f.client.HPAs[kube.Key(f.app.Metadata.Namespace, web.Metadata.Name)]
	if !ok || hpa.MinReplicas != 1 || hpa.MaxReplicas != 3 || hpa.TargetName != web.Metadata.Name {
		t.Fatalf("hpa=%+v", hpa)
	}
}

func TestOperator_ManualScaleUpdatesDesiredPolicyNotChildDirectly(t *testing.T) {
	f := newOperatorFixture(t)
	f.reconcile(t)
	oldDeployment := f.deployment("web")
	oldName := oldDeployment.Metadata.Name
	f.app.Spec.Identity.ReleaseID = "rel-2"
	f.app.Spec.Image.Digest = "sha256:" + strings.Repeat("b", 64)
	web := f.app.Spec.Processes["web"]
	web.MinReplicas, web.MaxReplicas = 2, 5
	f.app.Spec.Processes["web"] = web
	f.app.Metadata.Generation++
	f.client.PutPaaSApp(f.app)
	f.reconcile(t)
	newDeployment := f.deployment("web")
	if newDeployment.Metadata.Name == oldName || newDeployment.Spec.Replicas != nil {
		t.Fatalf("deployment=%+v old=%s", newDeployment, oldName)
	}
	hpa := f.client.HPAs[kube.Key(f.app.Metadata.Namespace, newDeployment.Metadata.Name)]
	if hpa.MinReplicas != 2 || hpa.MaxReplicas != 5 {
		t.Fatalf("hpa=%+v", hpa)
	}
	if old := f.client.Deployments[kube.Key(f.app.Metadata.Namespace, oldName)]; old.Metadata.Name == "" {
		t.Fatal("old child was mutated/deleted before new release readiness")
	}
}

func TestDrift_ChildMutationIsReconciledByOperator(t *testing.T) {
	f := newOperatorFixture(t)
	f.reconcile(t)
	key := kube.Key(f.app.Metadata.Namespace, deploymentName(f.app.Metadata.Name, "web", f.app.Spec.Identity.ReleaseID))
	drifted := f.client.Deployments[key]
	drifted.Spec.Pod.RuntimeClassName = ""
	drifted.Spec.Pod.Containers[0].Image = "attacker.invalid/root:latest"
	drifted.Spec.Pod.Containers[0].SecurityContext.AllowPrivilegeEscalation = true
	f.client.Deployments[key] = drifted
	f.reconcile(t)
	repaired := f.client.Deployments[key]
	if repaired.Spec.Pod.RuntimeClassName != "gvisor" || repaired.Spec.Pod.Containers[0].Image != f.app.Spec.Image.Reference() || repaired.Spec.Pod.Containers[0].SecurityContext.AllowPrivilegeEscalation {
		t.Fatalf("drift not repaired: %+v", repaired.Spec.Pod)
	}
}

func TestOperator_RejectsSpecMutationUnderSameReleaseID(t *testing.T) {
	f := newOperatorFixture(t)
	f.reconcile(t)
	f.app.Spec.Image.Digest = "sha256:" + strings.Repeat("b", 64)
	f.app.Metadata.Generation++
	f.client.PutPaaSApp(f.app)
	observed := f.reconcile(t)
	if observed.Status.Phase != "Degraded" || conditionReason(observed.Status.Conditions, "Ready") != "ReleaseIdentityConflict" {
		t.Fatalf("status=%+v", observed.Status)
	}
	if len(f.client.Deployments) != 2 {
		t.Fatalf("mutated spec created child resources: %d", len(f.client.Deployments))
	}
}

func TestOperator_LongNamesRemainUniqueAndDNSValid(t *testing.T) {
	f := newOperatorFixture(t)
	f.app.Metadata.Name = strings.Repeat("a", 40)
	f.app.Spec.Processes = map[string]runtimev1.ProcessSpec{
		"worker-alpha-very-long-process-name": {Command: []string{"./worker-a"}, MinReplicas: 1, MaxReplicas: 1},
		"worker-beta-very-long-process-name":  {Command: []string{"./worker-b"}, MinReplicas: 1, MaxReplicas: 1},
		"web":                                 {Port: 8080, MinReplicas: 1, MaxReplicas: 1},
	}
	f.client = kube.NewMemoryClient()
	f.reconciler.Client = f.client
	f.client.PutPaaSApp(f.app)
	f.reconcile(t)
	seen := map[string]bool{}
	for _, dep := range f.client.Deployments {
		if len(dep.Metadata.Name) > 63 || !runtimev1.ValidDNSLabel(dep.Metadata.Name) || seen[dep.Metadata.Name] {
			t.Fatalf("bad name=%q", dep.Metadata.Name)
		}
		seen[dep.Metadata.Name] = true
	}
	if len(seen) != 3 {
		t.Fatalf("names=%v", seen)
	}
}

func TestDelete_RemovesRouteBeforeWorkload(t *testing.T) {
	f := newOperatorFixture(t)
	f.reconcile(t)
	f.markAllReady()
	f.reconcile(t)
	f.app.Spec.Lifecycle.State = runtimev1.LifecycleDeletingRuntime
	f.app.Metadata.Generation++
	f.client.PutPaaSApp(f.app)
	before := len(f.client.Log.Values())
	f.reconcile(t)
	log := f.client.Log.Values()[before:]
	if len(log) < 2 || !strings.HasPrefix(log[0], "delete route ") {
		t.Fatalf("log=%v", log)
	}
	for _, op := range log {
		if strings.HasPrefix(op, "delete deployment ") {
			t.Fatalf("workload deleted in same pass as route: %v", log)
		}
	}
	if len(f.client.Deployments) == 0 {
		t.Fatal("workloads removed before route deletion was observed")
	}
}

func TestDelete_UsesRetentionStateBeforeNamespaceRemoval(t *testing.T) {
	f := newOperatorFixture(t)
	f.reconcile(t)
	f.app.Spec.Lifecycle.State = runtimev1.LifecycleDeletingRuntime
	f.app.Metadata.Generation++
	f.client.PutPaaSApp(f.app)
	f.reconcile(t)             // route
	observed := f.reconcile(t) // workloads/policy -> retaining
	if observed.Status.Phase != "Retaining" {
		t.Fatalf("status=%+v", observed.Status)
	}
	if strings.Contains(strings.Join(f.client.Log.Values(), "\n"), "delete namespace") {
		t.Fatal("operator must not own namespace deletion")
	}
}

func TestDelete_DoesNotDeleteAttachmentReferences(t *testing.T) {
	f := newOperatorFixture(t)
	f.app.Spec.AttachmentSnapshotRef = "bindings-v42"
	f.client.PutPaaSApp(f.app)
	f.reconcile(t)
	f.app.Spec.Lifecycle.State = runtimev1.LifecycleDeletingRuntime
	f.app.Metadata.Generation++
	f.client.PutPaaSApp(f.app)
	f.reconcile(t)
	observed := f.reconcile(t)
	if observed.Spec.AttachmentSnapshotRef != "bindings-v42" || strings.Contains(strings.Join(f.client.Log.Values(), "\n"), "attachment") {
		t.Fatalf("attachment reference changed/deleted: app=%+v log=%v", observed.Spec, f.client.Log.Values())
	}
}

func TestDelete_IsRecoverableBeforeFinalPurge(t *testing.T) {
	f := newOperatorFixture(t)
	f.reconcile(t)
	f.app.Spec.Lifecycle.State = runtimev1.LifecycleDeletingRuntime
	f.app.Metadata.Generation++
	f.client.PutPaaSApp(f.app)
	f.reconcile(t)
	if _, ok := f.client.Routes[kube.Key(f.app.Metadata.Namespace, f.app.Metadata.Name)]; ok {
		t.Fatal("route not removed")
	}
	f.app.Spec.Lifecycle.State = runtimev1.LifecycleActive
	f.app.Metadata.Generation++
	f.client.PutPaaSApp(f.app)
	observed := f.reconcile(t)
	if observed.Status.Phase != "Deploying" || len(f.client.Deployments) == 0 || len(f.client.Routes) != 1 {
		t.Fatalf("recovery failed status=%+v deployments=%d routes=%d", observed.Status, len(f.client.Deployments), len(f.client.Routes))
	}
}

func TestDelete_WaitsForAsynchronousResourceDeletion(t *testing.T) {
	f := newOperatorFixture(t)
	f.reconcile(t)
	f.app.Spec.Lifecycle.State = runtimev1.LifecycleDeletingRuntime
	f.app.Metadata.Generation++
	f.client.PutPaaSApp(f.app)
	f.reconcile(t) // remove route immediately
	f.client.DeferDeletion = true
	observed := f.reconcile(t)
	if observed.Status.Phase != "DeletingWorkloads" {
		t.Fatalf("status=%+v", observed.Status)
	}
	f.client.CompletePendingDeletions()
	observed = f.reconcile(t)
	if observed.Status.Phase != "DeletingPolicy" {
		t.Fatalf("status=%+v", observed.Status)
	}
	f.client.CompletePendingDeletions()
	observed = f.reconcile(t)
	if observed.Status.Phase != "Retaining" {
		t.Fatalf("status=%+v", observed.Status)
	}
}

func cloneDeploymentMap(in map[string]kube.Deployment) map[string]kube.Deployment {
	out := map[string]kube.Deployment{}
	for k, v := range in {
		out[k] = v
	}
	return out
}
func cloneServiceMap(in map[string]kube.Service) map[string]kube.Service {
	out := map[string]kube.Service{}
	for k, v := range in {
		out[k] = v
	}
	return out
}
func cloneHPAMap(in map[string]kube.HPA) map[string]kube.HPA {
	out := map[string]kube.HPA{}
	for k, v := range in {
		out[k] = v
	}
	return out
}
func cloneRouteMap(in map[string]kube.HTTPRoute) map[string]kube.HTTPRoute {
	out := map[string]kube.HTTPRoute{}
	for k, v := range in {
		out[k] = v
	}
	return out
}
func clonePolicyMap(in map[string]kube.NetworkPolicy) map[string]kube.NetworkPolicy {
	out := map[string]kube.NetworkPolicy{}
	for k, v := range in {
		out[k] = v
	}
	return out
}
func countLog(values []string, prefix string) int {
	n := 0
	for _, v := range values {
		if strings.HasPrefix(v, prefix) {
			n++
		}
	}
	return n
}
func conditionReason(conditions []runtimev1.Condition, typ string) string {
	for _, c := range conditions {
		if c.Type == typ {
			return c.Reason
		}
	}
	return ""
}
