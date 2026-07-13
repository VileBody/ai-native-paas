package kubeapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"sort"

	"github.com/keir-research/ai-native-paas/internal/runtime/application"
	"github.com/keir-research/ai-native-paas/internal/runtime/kube"
	runtimev1 "github.com/keir-research/ai-native-paas/pkg/contracts/runtime/v1"
)

func labelsForPod(metadata kube.Metadata) map[string]string { return cloneStringMap(metadata.Labels) }
func securityContext(value kube.SecurityContext) map[string]any {
	return map[string]any{
		"runAsNonRoot":             value.RunAsNonRoot,
		"allowPrivilegeEscalation": value.AllowPrivilegeEscalation,
		"readOnlyRootFilesystem":   value.ReadOnlyRootFilesystem,
		"seccompProfile":           map[string]any{"type": value.SeccompProfile},
		"capabilities":             map[string]any{"drop": append([]string(nil), value.DropCapabilities...)},
	}
}
func probe(path string, port, timeout int, startup bool) map[string]any {
	if path == "" || port <= 0 {
		return nil
	}
	period := 2
	if !startup {
		period = 5
	}
	value := map[string]any{"httpGet": map[string]any{"path": path, "port": port}, "periodSeconds": period, "timeoutSeconds": 2, "failureThreshold": failureThreshold(timeout, period)}
	if !startup {
		value["successThreshold"] = 1
	}
	return value
}
func podSpec(value kube.PodSpec) map[string]any {
	containers := make([]any, 0, len(value.Containers))
	for _, item := range value.Containers {
		container := map[string]any{
			"name": item.Name, "image": item.Image,
			"securityContext": securityContext(item.SecurityContext),
			"resources":       map[string]any{"requests": quantityMap(item.Resources.Requests), "limits": quantityMap(item.Resources.Limits)},
		}
		if len(item.Command) > 0 {
			container["command"] = append([]string(nil), item.Command...)
		}
		if item.Port > 0 {
			container["ports"] = []any{map[string]any{"name": "http", "containerPort": item.Port}}
		}
		if p := probe(item.HealthPath, item.Port, item.StartupTimeout, true); p != nil {
			container["startupProbe"] = p
		}
		if p := probe(item.HealthPath, item.Port, item.ReadinessTimeout, false); p != nil {
			container["readinessProbe"] = p
		}
		if len(item.VolumeMounts) > 0 {
			mounts := make([]any, 0, len(item.VolumeMounts))
			for _, m := range item.VolumeMounts {
				mounts = append(mounts, map[string]any{"name": m.Name, "mountPath": m.MountPath})
			}
			container["volumeMounts"] = mounts
		}
		containers = append(containers, container)
	}
	out := map[string]any{"automountServiceAccountToken": value.AutomountServiceAccountToken, "containers": containers, "enableServiceLinks": false}
	if value.RuntimeClassName != "" {
		out["runtimeClassName"] = value.RuntimeClassName
	}
	if len(value.Volumes) > 0 {
		volumes := make([]any, 0, len(value.Volumes))
		for _, v := range value.Volumes {
			empty := map[string]any{}
			if v.EmptyDirSizeLimit != "" {
				empty["sizeLimit"] = v.EmptyDirSizeLimit
			}
			volumes = append(volumes, map[string]any{"name": v.Name, "emptyDir": empty})
		}
		out["volumes"] = volumes
	}
	return out
}

func (c *Client) UpsertDeployment(ctx context.Context, value kube.Deployment) error {
	labels := labelsForPod(value.Metadata)
	selector := map[string]string{"platform.example.com/release-id": labels["platform.example.com/release-id"], "platform.example.com/process": labels["platform.example.com/process"]}
	spec := map[string]any{"selector": map[string]any{"matchLabels": selector}, "progressDeadlineSeconds": value.Spec.ProgressDeadlineSeconds, "template": map[string]any{"metadata": map[string]any{"labels": labels}, "spec": podSpec(value.Spec.Pod)}}
	if value.Spec.Replicas != nil {
		spec["replicas"] = *value.Spec.Replicas
	}
	object := map[string]any{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": metadata(value.Metadata), "spec": spec}
	return c.apply(ctx, c.endpoint("apis", "apps", "v1", "namespaces", value.Metadata.Namespace, "deployments", value.Metadata.Name), object)
}
func decodeDeployment(object map[string]any) kube.Deployment {
	meta := metadataFromObject(object)
	spec := nested(object, "spec")
	status := nested(object, "status")
	failed, message := statusConditionFailed(status)
	var replicas *int
	if raw, ok := spec["replicas"]; ok {
		x := intValue(raw)
		replicas = &x
	}
	return kube.Deployment{Metadata: meta, Spec: kube.DeploymentSpec{Replicas: replicas, ProgressDeadlineSeconds: intValue(spec["progressDeadlineSeconds"])}, Status: kube.DeploymentStatus{ReadyReplicas: intValue(status["readyReplicas"]), Failed: failed, Message: message}}
}
func (c *Client) GetDeployment(ctx context.Context, namespace, name string) (kube.Deployment, bool, error) {
	var raw json.RawMessage
	found, err := c.do(ctx, http.MethodGet, c.endpoint("apis", "apps", "v1", "namespaces", namespace, "deployments", name), "", nil, nil, &raw)
	if err != nil || !found {
		return kube.Deployment{}, found, err
	}
	object, err := decodeObject(raw)
	if err != nil {
		return kube.Deployment{}, false, err
	}
	return decodeDeployment(object), true, nil
}
func (c *Client) ListDeployments(ctx context.Context, namespace string, labels map[string]string) ([]kube.Deployment, error) {
	var raw map[string]any
	_, err := c.do(ctx, http.MethodGet, c.endpoint("apis", "apps", "v1", "namespaces", namespace, "deployments"), "", url.Values{"labelSelector": {labelSelector(labels)}}, nil, &raw)
	if err != nil {
		return nil, err
	}
	out := []kube.Deployment{}
	for _, o := range objectList(raw) {
		out = append(out, decodeDeployment(o))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Metadata.Name < out[j].Metadata.Name })
	return out, nil
}
func (c *Client) DeleteDeployment(ctx context.Context, namespace, name string) error {
	return c.delete(ctx, c.endpoint("apis", "apps", "v1", "namespaces", namespace, "deployments", name))
}

func (c *Client) UpsertService(ctx context.Context, value kube.Service) error {
	object := map[string]any{"apiVersion": "v1", "kind": "Service", "metadata": metadata(value.Metadata), "spec": map[string]any{"type": "ClusterIP", "selector": cloneStringMap(value.Selector), "ports": []any{map[string]any{"name": "http", "port": value.Port, "targetPort": value.Port}}}}
	return c.apply(ctx, c.endpoint("api", "v1", "namespaces", value.Metadata.Namespace, "services", value.Metadata.Name), object)
}
func decodeService(o map[string]any) kube.Service {
	meta := metadataFromObject(o)
	spec := nested(o, "spec")
	selector := map[string]string{}
	for k, v := range mapStringAny(spec["selector"]) {
		selector[k] = stringValue(v)
	}
	port := 0
	if list, ok := spec["ports"].([]any); ok && len(list) > 0 {
		port = intValue(mapStringAny(list[0])["port"])
	}
	return kube.Service{Metadata: meta, Port: port, Selector: selector}
}
func (c *Client) GetService(ctx context.Context, namespace, name string) (kube.Service, bool, error) {
	var raw json.RawMessage
	found, err := c.do(ctx, http.MethodGet, c.endpoint("api", "v1", "namespaces", namespace, "services", name), "", nil, nil, &raw)
	if err != nil || !found {
		return kube.Service{}, found, err
	}
	o, err := decodeObject(raw)
	if err != nil {
		return kube.Service{}, false, err
	}
	return decodeService(o), true, nil
}
func (c *Client) ListServices(ctx context.Context, namespace string, labels map[string]string) ([]kube.Service, error) {
	var raw map[string]any
	_, err := c.do(ctx, http.MethodGet, c.endpoint("api", "v1", "namespaces", namespace, "services"), "", url.Values{"labelSelector": {labelSelector(labels)}}, nil, &raw)
	if err != nil {
		return nil, err
	}
	out := []kube.Service{}
	for _, o := range objectList(raw) {
		out = append(out, decodeService(o))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Metadata.Name < out[j].Metadata.Name })
	return out, nil
}
func (c *Client) DeleteService(ctx context.Context, namespace, name string) error {
	return c.delete(ctx, c.endpoint("api", "v1", "namespaces", namespace, "services", name))
}

func (c *Client) UpsertHPA(ctx context.Context, value kube.HPA) error {
	object := map[string]any{"apiVersion": "autoscaling/v2", "kind": "HorizontalPodAutoscaler", "metadata": metadata(value.Metadata), "spec": map[string]any{"scaleTargetRef": map[string]any{"apiVersion": "apps/v1", "kind": "Deployment", "name": value.TargetName}, "minReplicas": value.MinReplicas, "maxReplicas": value.MaxReplicas, "metrics": []any{map[string]any{"type": "Resource", "resource": map[string]any{"name": "cpu", "target": map[string]any{"type": "Utilization", "averageUtilization": 70}}}}}}
	return c.apply(ctx, c.endpoint("apis", "autoscaling", "v2", "namespaces", value.Metadata.Namespace, "horizontalpodautoscalers", value.Metadata.Name), object)
}
func decodeHPA(o map[string]any) kube.HPA {
	meta := metadataFromObject(o)
	spec := nested(o, "spec")
	target := mapStringAny(spec["scaleTargetRef"])
	return kube.HPA{Metadata: meta, TargetName: stringValue(target["name"]), MinReplicas: intValue(spec["minReplicas"]), MaxReplicas: intValue(spec["maxReplicas"])}
}
func (c *Client) ListHPAs(ctx context.Context, namespace string, labels map[string]string) ([]kube.HPA, error) {
	var raw map[string]any
	_, err := c.do(ctx, http.MethodGet, c.endpoint("apis", "autoscaling", "v2", "namespaces", namespace, "horizontalpodautoscalers"), "", url.Values{"labelSelector": {labelSelector(labels)}}, nil, &raw)
	if err != nil {
		return nil, err
	}
	out := []kube.HPA{}
	for _, o := range objectList(raw) {
		out = append(out, decodeHPA(o))
	}
	return out, nil
}
func (c *Client) DeleteHPA(ctx context.Context, namespace, name string) error {
	return c.delete(ctx, c.endpoint("apis", "autoscaling", "v2", "namespaces", namespace, "horizontalpodautoscalers", name))
}

func (c *Client) UpsertJob(ctx context.Context, value kube.Job) error {
	spec := map[string]any{"backoffLimit": 0, "template": map[string]any{"metadata": map[string]any{"labels": labelsForPod(value.Metadata)}, "spec": podSpec(value.Pod)}}
	if value.TimeoutSeconds > 0 {
		spec["activeDeadlineSeconds"] = value.TimeoutSeconds
	}
	object := map[string]any{"apiVersion": "batch/v1", "kind": "Job", "metadata": metadata(value.Metadata), "spec": spec}
	return c.apply(ctx, c.endpoint("apis", "batch", "v1", "namespaces", value.Metadata.Namespace, "jobs", value.Metadata.Name), object)
}
func decodeJob(o map[string]any) kube.Job {
	meta := metadataFromObject(o)
	status := nested(o, "status")
	value := kube.JobPending
	message := ""
	if intValue(status["succeeded"]) > 0 {
		value = kube.JobSucceeded
	} else if intValue(status["failed"]) > 0 {
		value = kube.JobFailed
		_, message = statusConditionFailed(status)
	}
	return kube.Job{Metadata: meta, Status: value, Message: message}
}
func (c *Client) GetJob(ctx context.Context, namespace, name string) (kube.Job, bool, error) {
	var raw json.RawMessage
	found, err := c.do(ctx, http.MethodGet, c.endpoint("apis", "batch", "v1", "namespaces", namespace, "jobs", name), "", nil, nil, &raw)
	if err != nil || !found {
		return kube.Job{}, found, err
	}
	o, err := decodeObject(raw)
	if err != nil {
		return kube.Job{}, false, err
	}
	return decodeJob(o), true, nil
}
func (c *Client) ListJobs(ctx context.Context, namespace string, labels map[string]string) ([]kube.Job, error) {
	var raw map[string]any
	_, err := c.do(ctx, http.MethodGet, c.endpoint("apis", "batch", "v1", "namespaces", namespace, "jobs"), "", url.Values{"labelSelector": {labelSelector(labels)}}, nil, &raw)
	if err != nil {
		return nil, err
	}
	out := []kube.Job{}
	for _, o := range objectList(raw) {
		out = append(out, decodeJob(o))
	}
	return out, nil
}
func (c *Client) DeleteJob(ctx context.Context, namespace, name string) error {
	return c.delete(ctx, c.endpoint("apis", "batch", "v1", "namespaces", namespace, "jobs", name))
}

func (c *Client) UpsertHTTPRoute(ctx context.Context, value kube.HTTPRoute) error {
	spec := map[string]any{"parentRefs": []any{map[string]any{"name": c.gatewayName, "namespace": c.gatewayNamespace}}, "hostnames": []string{value.Hostname}, "rules": []any{}}
	if value.TrafficEnabled && value.BackendService != "" && value.BackendPort > 0 {
		spec["rules"] = []any{map[string]any{"backendRefs": []any{map[string]any{"name": value.BackendService, "port": value.BackendPort}}}}
	}
	object := map[string]any{"apiVersion": "gateway.networking.k8s.io/v1", "kind": "HTTPRoute", "metadata": metadata(value.Metadata), "spec": spec}
	return c.apply(ctx, c.endpoint("apis", "gateway.networking.k8s.io", "v1", "namespaces", value.Metadata.Namespace, "httproutes", value.Metadata.Name), object)
}
func decodeRoute(o map[string]any) kube.HTTPRoute {
	meta := metadataFromObject(o)
	spec := nested(o, "spec")
	hostname := ""
	if list, ok := spec["hostnames"].([]any); ok && len(list) > 0 {
		hostname = stringValue(list[0])
	}
	route := kube.HTTPRoute{Metadata: meta, Hostname: hostname}
	if rules, ok := spec["rules"].([]any); ok && len(rules) > 0 {
		refs, _ := mapStringAny(rules[0])["backendRefs"].([]any)
		if len(refs) > 0 {
			ref := mapStringAny(refs[0])
			route.BackendService = stringValue(ref["name"])
			route.BackendPort = intValue(ref["port"])
			route.TrafficEnabled = route.BackendService != "" && route.BackendPort > 0
		}
	}
	return route
}
func (c *Client) GetHTTPRoute(ctx context.Context, namespace, name string) (kube.HTTPRoute, bool, error) {
	var raw json.RawMessage
	found, err := c.do(ctx, http.MethodGet, c.endpoint("apis", "gateway.networking.k8s.io", "v1", "namespaces", namespace, "httproutes", name), "", nil, nil, &raw)
	if err != nil || !found {
		return kube.HTTPRoute{}, found, err
	}
	o, err := decodeObject(raw)
	if err != nil {
		return kube.HTTPRoute{}, false, err
	}
	return decodeRoute(o), true, nil
}
func (c *Client) DeleteHTTPRoute(ctx context.Context, namespace, name string) error {
	return c.delete(ctx, c.endpoint("apis", "gateway.networking.k8s.io", "v1", "namespaces", namespace, "httproutes", name))
}

func selectorTerm(namespace string, labels map[string]string) map[string]any {
	return map[string]any{"namespaceSelector": map[string]any{"matchLabels": map[string]string{"kubernetes.io/metadata.name": namespace}}, "podSelector": map[string]any{"matchLabels": labels}}
}
func (c *Client) UpsertNetworkPolicy(ctx context.Context, value kube.NetworkPolicy) error {
	appID := value.Metadata.Labels["platform.example.com/application-id"]
	podSelector := map[string]any{"matchLabels": map[string]string{"platform.example.com/application-id": appID}}
	ingress := []any{map[string]any{"from": []any{selectorTerm(c.gatewayNamespace, c.gatewayPodLabels)}}}
	egress := []any{map[string]any{"to": []any{selectorTerm(c.dnsNamespace, c.dnsPodLabels)}, "ports": []any{map[string]any{"protocol": "UDP", "port": 53}, map[string]any{"protocol": "TCP", "port": 53}}}}
	if len(value.AllowedEgress) > 0 && value.AllowedEgress[0] == "public-default" {
		egress = append(egress, map[string]any{})
	}
	object := map[string]any{"apiVersion": "networking.k8s.io/v1", "kind": "NetworkPolicy", "metadata": metadata(value.Metadata), "spec": map[string]any{"podSelector": podSelector, "policyTypes": []string{"Ingress", "Egress"}, "ingress": ingress, "egress": egress}}
	return c.apply(ctx, c.endpoint("apis", "networking.k8s.io", "v1", "namespaces", value.Metadata.Namespace, "networkpolicies", value.Metadata.Name), object)
}
func decodePolicy(o map[string]any) kube.NetworkPolicy {
	return kube.NetworkPolicy{Metadata: metadataFromObject(o), DefaultDeny: true}
}
func (c *Client) GetNetworkPolicy(ctx context.Context, namespace, name string) (kube.NetworkPolicy, bool, error) {
	var raw json.RawMessage
	found, err := c.do(ctx, http.MethodGet, c.endpoint("apis", "networking.k8s.io", "v1", "namespaces", namespace, "networkpolicies", name), "", nil, nil, &raw)
	if err != nil || !found {
		return kube.NetworkPolicy{}, found, err
	}
	o, err := decodeObject(raw)
	if err != nil {
		return kube.NetworkPolicy{}, false, err
	}
	return decodePolicy(o), true, nil
}
func (c *Client) DeleteNetworkPolicy(ctx context.Context, namespace, name string) error {
	return c.delete(ctx, c.endpoint("apis", "networking.k8s.io", "v1", "namespaces", namespace, "networkpolicies", name))
}

func (c *Client) GetPaaSApp(ctx context.Context, ref application.RuntimeObjectRef) (runtimev1.PaaSApp, bool, error) {
	var value runtimev1.PaaSApp
	found, err := c.do(ctx, http.MethodGet, c.endpoint("apis", "platform.example.com", "v1alpha1", "namespaces", ref.Namespace, "paasapps", ref.Name), "", nil, nil, &value)
	return value, found, err
}
func (c *Client) ListPaaSApps(ctx context.Context) ([]runtimev1.PaaSApp, error) {
	var raw struct {
		Items []runtimev1.PaaSApp `json:"items"`
	}
	_, err := c.do(ctx, http.MethodGet, c.endpoint("apis", "platform.example.com", "v1alpha1", "paasapps"), "", nil, nil, &raw)
	return raw.Items, err
}
func (c *Client) UpdatePaaSAppStatus(ctx context.Context, namespace, name string, status runtimev1.PaaSAppStatus) error {
	_, err := c.do(ctx, http.MethodPatch, c.endpoint("apis", "platform.example.com", "v1alpha1", "namespaces", namespace, "paasapps", name, "status"), "application/merge-patch+json", nil, map[string]any{"status": status}, nil)
	return err
}

var _ kube.Client = (*Client)(nil)
var _ application.RuntimeObserver = (*Client)(nil)
