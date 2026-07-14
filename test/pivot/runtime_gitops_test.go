package pivot_test

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/keir-research/ai-native-paas/internal/runtime/kube"
	"github.com/keir-research/ai-native-paas/internal/runtime/operator"
	runtimev1 "github.com/keir-research/ai-native-paas/pkg/contracts/runtime/v1"
)

type runtimeFixedClock struct{ now time.Time }

func (c runtimeFixedClock) Now() time.Time { return c.now }

func TestRuntime_PaaSAppFastPathProducesEquivalentStandardResources(t *testing.T) {
	t.Parallel()

	app := runtimev1.PaaSApp{
		TypeMeta: runtimev1.TypeMeta{APIVersion: runtimev1.APIVersion, Kind: runtimev1.Kind},
		Metadata: runtimev1.ObjectMeta{Name: "web-production", Namespace: "app-web-production", UID: "uid-web", Generation: 1},
		Spec: runtimev1.PaaSAppSpec{
			Identity: runtimev1.IdentitySpec{TenantID: "tenant-1", ProjectID: "project-1", ApplicationID: "app-1", EnvironmentID: "env-1", Environment: "production", ReleaseID: "release-1"},
			Image: runtimev1.ImageSpec{
				Repository: "registry.test/tenants/tenant-1/apps/app-1",
				Digest:     "sha256:" + strings.Repeat("a", 64),
				MediaType:  "application/vnd.oci.image.manifest.v1+json",
			},
			Runtime: runtimev1.RuntimeSpec{Isolation: runtimev1.IsolationSandboxed, Unit: "small"},
			Processes: map[string]runtimev1.ProcessSpec{
				"web": {Port: 8080, MinReplicas: 1, MaxReplicas: 3, HealthPath: "/health", StartupTimeout: 60, ReadinessTimeout: 30},
			},
			Release:               runtimev1.ReleaseSpec{Strategy: "Rolling", RolloutTimeout: 300},
			Route:                 runtimev1.RouteSpec{GeneratedHostname: "web-production.apps.test", Process: "web"},
			AttachmentSnapshotRef: "inputs-v1",
			Network:               runtimev1.NetworkSpec{EgressProfile: "public-default"},
			Lifecycle:             runtimev1.LifecycleSpec{State: runtimev1.LifecycleActive},
		},
	}
	resources := runtimev1.ResourceQuantity{CPU: "250m", Memory: "512Mi", EphemeralStorage: "1Gi"}
	standard, err := operator.RenderStandardResources(app, resources)
	if err != nil {
		t.Fatal(err)
	}

	client := kube.NewMemoryClient()
	client.PutPaaSApp(app)
	reconciler := operator.Reconciler{
		Client: client,
		Clock:  runtimeFixedClock{now: time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)},
		Units:  map[string]runtimev1.ResourceQuantity{"small": resources},
	}
	if err = reconciler.Reconcile(context.Background(), app); err != nil {
		t.Fatal(err)
	}

	wantDeployments := map[string]kube.Deployment{}
	for _, value := range standard.Deployments {
		wantDeployments[kube.Key(value.Metadata.Namespace, value.Metadata.Name)] = value
	}
	wantServices := map[string]kube.Service{}
	for _, value := range standard.Services {
		wantServices[kube.Key(value.Metadata.Namespace, value.Metadata.Name)] = value
	}
	wantHPAs := map[string]kube.HPA{}
	for _, value := range standard.HPAs {
		wantHPAs[kube.Key(value.Metadata.Namespace, value.Metadata.Name)] = value
	}
	if !reflect.DeepEqual(client.Deployments, wantDeployments) || !reflect.DeepEqual(client.Services, wantServices) || !reflect.DeepEqual(client.HPAs, wantHPAs) {
		t.Fatalf("PaaSApp children differ from standard resources: deployments=%+v services=%+v hpas=%+v", client.Deployments, client.Services, client.HPAs)
	}
	if !reflect.DeepEqual(client.Routes[kube.Key(app.Metadata.Namespace, app.Metadata.Name)], standard.HTTPRoute) ||
		!reflect.DeepEqual(client.Policies[kube.Key(app.Metadata.Namespace, "platform-default-deny")], standard.NetworkPolicy) {
		t.Fatalf("PaaSApp network resources differ from standard resources")
	}

	deployment := standard.Deployments["web"]
	container := deployment.Spec.Pod.Containers[0]
	if deployment.Metadata.Namespace != app.Metadata.Namespace || container.Image != app.Spec.Image.Reference() ||
		deployment.Spec.Pod.RuntimeClassName != "gvisor" || deployment.Spec.Pod.AutomountServiceAccountToken ||
		!container.SecurityContext.RunAsNonRoot || container.SecurityContext.AllowPrivilegeEscalation ||
		!container.SecurityContext.ReadOnlyRootFilesystem || container.SecurityContext.SeccompProfile != "RuntimeDefault" {
		t.Fatalf("standard workload policy mismatch: %+v", deployment)
	}
	observed, ok := client.GetPaaSAppObject(app.Metadata.Namespace, app.Metadata.Name)
	if !ok || observed.Status.ObservedGeneration != app.Metadata.Generation || observed.Status.Phase != "Deploying" {
		t.Fatalf("status policy mismatch: %+v", observed.Status)
	}
}
