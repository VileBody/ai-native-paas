package operator

import (
	"strings"

	"github.com/keir-research/ai-native-paas/internal/runtime/domain"
	"github.com/keir-research/ai-native-paas/internal/runtime/kube"
	runtimev1 "github.com/keir-research/ai-native-paas/pkg/contracts/runtime/v1"
)

// StandardResources is the provider-neutral Kubernetes resource set emitted by
// the optional PaaSApp fast path. Reconcile consumes this exact value so the
// adapter cannot bypass the standard image, namespace, workload-security,
// routing, autoscaling, or network-policy representation.
type StandardResources struct {
	Deployments   map[string]kube.Deployment
	Services      map[string]kube.Service
	HPAs          map[string]kube.HPA
	HTTPRoute     kube.HTTPRoute
	NetworkPolicy kube.NetworkPolicy
}

func RenderStandardResources(app runtimev1.PaaSApp, resources runtimev1.ResourceQuantity) (StandardResources, error) {
	if err := app.Validate(); err != nil {
		return StandardResources{}, domain.Wrap(domain.CodeInvalidArgument, "reject invalid PaaSApp", err)
	}
	if strings.TrimSpace(resources.CPU) == "" || strings.TrimSpace(resources.Memory) == "" || strings.TrimSpace(resources.EphemeralStorage) == "" {
		return StandardResources{}, domain.NewError(domain.CodeInvalidArgument, "runtime resource unit is incomplete")
	}
	labels := baseLabels(app)
	owner := ownerReference(app)
	result := StandardResources{
		Deployments: map[string]kube.Deployment{},
		Services:    map[string]kube.Service{},
		HPAs:        map[string]kube.HPA{},
		HTTPRoute: kube.HTTPRoute{
			Metadata:       kube.Metadata{Name: app.Metadata.Name, Namespace: app.Metadata.Namespace, Labels: labels, OwnerReferences: []runtimev1.OwnerReference{owner}},
			Hostname:       app.Spec.Route.GeneratedHostname,
			BackendService: serviceName(app.Metadata.Name, app.Spec.Route.Process, app.Spec.Identity.ReleaseID),
			BackendPort:    app.Spec.Processes[app.Spec.Route.Process].Port,
		},
		NetworkPolicy: kube.NetworkPolicy{
			Metadata:       kube.Metadata{Name: "platform-default-deny", Namespace: app.Metadata.Namespace, Labels: labels, OwnerReferences: []runtimev1.OwnerReference{owner}},
			DefaultDeny:    true,
			AllowedIngress: []string{"platform-gateway"},
			AllowedEgress:  []string{app.Spec.Network.EgressProfile},
		},
	}
	for _, name := range app.SortedProcessNames() {
		process := app.Spec.Processes[name]
		deployment := desiredDeployment(app, name, process, resources, labels, owner)
		result.Deployments[name] = deployment
		if process.Port > 0 {
			result.Services[name] = kube.Service{
				Metadata: kube.Metadata{Name: serviceName(app.Metadata.Name, name, app.Spec.Identity.ReleaseID), Namespace: app.Metadata.Namespace, Labels: labels, OwnerReferences: []runtimev1.OwnerReference{owner}},
				Port:     process.Port,
				Selector: map[string]string{"platform.example.com/release-id": app.Spec.Identity.ReleaseID, "platform.example.com/process": name},
			}
		}
		if process.MaxReplicas > process.MinReplicas {
			result.HPAs[name] = kube.HPA{
				Metadata:    kube.Metadata{Name: deployment.Metadata.Name, Namespace: app.Metadata.Namespace, Labels: cloneLabels(deployment.Metadata.Labels), OwnerReferences: []runtimev1.OwnerReference{owner}},
				TargetName:  deployment.Metadata.Name,
				MinReplicas: process.MinReplicas,
				MaxReplicas: process.MaxReplicas,
			}
		}
	}
	return result, nil
}
