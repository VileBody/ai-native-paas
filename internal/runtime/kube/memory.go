package kube

import (
	"context"
	"sort"
	"sync"

	runtimev1 "github.com/keir-research/ai-native-paas/pkg/contracts/runtime/v1"
)

type MemoryClient struct {
	mu            sync.Mutex
	Deployments   map[string]Deployment
	Services      map[string]Service
	HPAs          map[string]HPA
	Jobs          map[string]Job
	Routes        map[string]HTTPRoute
	Policies      map[string]NetworkPolicy
	Apps          map[string]runtimev1.PaaSApp
	Log           *OperationLog
	DeferDeletion bool
	pending       map[string]pendingDeletion
}

func NewMemoryClient() *MemoryClient {
	return &MemoryClient{Deployments: map[string]Deployment{}, Services: map[string]Service{}, HPAs: map[string]HPA{}, Jobs: map[string]Job{}, Routes: map[string]HTTPRoute{}, Policies: map[string]NetworkPolicy{}, Apps: map[string]runtimev1.PaaSApp{}, Log: &OperationLog{}, pending: map[string]pendingDeletion{}}
}
func (c *MemoryClient) ensure() {
	if c.Deployments == nil {
		c.Deployments = map[string]Deployment{}
	}
	if c.Services == nil {
		c.Services = map[string]Service{}
	}
	if c.HPAs == nil {
		c.HPAs = map[string]HPA{}
	}
	if c.Jobs == nil {
		c.Jobs = map[string]Job{}
	}
	if c.Routes == nil {
		c.Routes = map[string]HTTPRoute{}
	}
	if c.Policies == nil {
		c.Policies = map[string]NetworkPolicy{}
	}
	if c.Apps == nil {
		c.Apps = map[string]runtimev1.PaaSApp{}
	}
	if c.Log == nil {
		c.Log = &OperationLog{}
	}
	if c.pending == nil {
		c.pending = map[string]pendingDeletion{}
	}
}
func (c *MemoryClient) PutPaaSApp(app runtimev1.PaaSApp) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ensure()
	c.Apps[Key(app.Metadata.Namespace, app.Metadata.Name)] = app
}
func (c *MemoryClient) GetPaaSAppObject(namespace, name string) (runtimev1.PaaSApp, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ensure()
	v, ok := c.Apps[Key(namespace, name)]
	return v, ok
}

type pendingDeletion struct {
	Kind string
	Key  string
}

func (c *MemoryClient) CompletePendingDeletions() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for pendingKey, item := range c.pending {
		switch item.Kind {
		case "deployment":
			delete(c.Deployments, item.Key)
		case "service":
			delete(c.Services, item.Key)
		case "hpa":
			delete(c.HPAs, item.Key)
		case "job":
			delete(c.Jobs, item.Key)
		case "route":
			delete(c.Routes, item.Key)
		case "networkpolicy":
			delete(c.Policies, item.Key)
		}
		delete(c.pending, pendingKey)
	}
}
func (c *MemoryClient) SetDeploymentStatus(namespace, name string, status DeploymentStatus) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v := c.Deployments[Key(namespace, name)]
	v.Status = status
	c.Deployments[Key(namespace, name)] = v
}
func (c *MemoryClient) SetJobStatus(namespace, name string, status JobStatus, message string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v := c.Jobs[Key(namespace, name)]
	v.Status = status
	v.Message = message
	c.Jobs[Key(namespace, name)] = v
}
func cloneDeployment(v Deployment) Deployment {
	v.Metadata = CloneMetadata(v.Metadata)
	if v.Spec.Replicas != nil {
		x := *v.Spec.Replicas
		v.Spec.Replicas = &x
	}
	v.Spec.Pod.Containers = append([]Container(nil), v.Spec.Pod.Containers...)
	for i := range v.Spec.Pod.Containers {
		v.Spec.Pod.Containers[i].Command = append([]string(nil), v.Spec.Pod.Containers[i].Command...)
		v.Spec.Pod.Containers[i].SecurityContext.DropCapabilities = append([]string(nil), v.Spec.Pod.Containers[i].SecurityContext.DropCapabilities...)
		v.Spec.Pod.Containers[i].VolumeMounts = append([]VolumeMount(nil), v.Spec.Pod.Containers[i].VolumeMounts...)
	}
	v.Spec.Pod.Volumes = append([]Volume(nil), v.Spec.Pod.Volumes...)
	return v
}
func (c *MemoryClient) UpsertDeployment(ctx context.Context, v Deployment) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ensure()
	k := Key(v.Metadata.Namespace, v.Metadata.Name)
	if old, ok := c.Deployments[k]; ok {
		v.Status = old.Status
	}
	c.Deployments[k] = cloneDeployment(v)
	c.Log.Add("upsert deployment " + k)
	return nil
}
func (c *MemoryClient) GetDeployment(ctx context.Context, ns, name string) (Deployment, bool, error) {
	if err := ctx.Err(); err != nil {
		return Deployment{}, false, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ensure()
	v, ok := c.Deployments[Key(ns, name)]
	return cloneDeployment(v), ok, nil
}
func (c *MemoryClient) ListDeployments(ctx context.Context, ns string, labels map[string]string) ([]Deployment, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	out := []Deployment{}
	for _, v := range c.Deployments {
		if v.Metadata.Namespace == ns && LabelsMatch(v.Metadata.Labels, labels) {
			out = append(out, cloneDeployment(v))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Metadata.Name < out[j].Metadata.Name })
	return out, nil
}
func (c *MemoryClient) DeleteDeployment(ctx context.Context, ns, name string) error {
	return c.delete(ctx, "deployment", Key(ns, name), func() { delete(c.Deployments, Key(ns, name)) })
}
func (c *MemoryClient) UpsertService(ctx context.Context, v Service) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ensure()
	v.Metadata = CloneMetadata(v.Metadata)
	v.Selector = CloneLabels(v.Selector)
	c.Services[Key(v.Metadata.Namespace, v.Metadata.Name)] = v
	c.Log.Add("upsert service " + Key(v.Metadata.Namespace, v.Metadata.Name))
	return nil
}
func (c *MemoryClient) GetService(ctx context.Context, ns, name string) (Service, bool, error) {
	if err := ctx.Err(); err != nil {
		return Service{}, false, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.Services[Key(ns, name)]
	v.Metadata = CloneMetadata(v.Metadata)
	v.Selector = CloneLabels(v.Selector)
	return v, ok, nil
}
func (c *MemoryClient) ListServices(ctx context.Context, ns string, labels map[string]string) ([]Service, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	out := []Service{}
	for _, v := range c.Services {
		if v.Metadata.Namespace == ns && LabelsMatch(v.Metadata.Labels, labels) {
			v.Metadata = CloneMetadata(v.Metadata)
			v.Selector = CloneLabels(v.Selector)
			out = append(out, v)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Metadata.Name < out[j].Metadata.Name })
	return out, nil
}
func (c *MemoryClient) DeleteService(ctx context.Context, ns, name string) error {
	return c.delete(ctx, "service", Key(ns, name), func() { delete(c.Services, Key(ns, name)) })
}
func (c *MemoryClient) UpsertHPA(ctx context.Context, v HPA) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	v.Metadata = CloneMetadata(v.Metadata)
	c.HPAs[Key(v.Metadata.Namespace, v.Metadata.Name)] = v
	c.Log.Add("upsert hpa " + Key(v.Metadata.Namespace, v.Metadata.Name))
	return nil
}
func (c *MemoryClient) ListHPAs(ctx context.Context, ns string, labels map[string]string) ([]HPA, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	out := []HPA{}
	for _, v := range c.HPAs {
		if v.Metadata.Namespace == ns && LabelsMatch(v.Metadata.Labels, labels) {
			v.Metadata = CloneMetadata(v.Metadata)
			out = append(out, v)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Metadata.Name < out[j].Metadata.Name })
	return out, nil
}
func (c *MemoryClient) DeleteHPA(ctx context.Context, ns, name string) error {
	return c.delete(ctx, "hpa", Key(ns, name), func() { delete(c.HPAs, Key(ns, name)) })
}
func (c *MemoryClient) UpsertJob(ctx context.Context, v Job) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ensure()
	k := Key(v.Metadata.Namespace, v.Metadata.Name)
	if old, ok := c.Jobs[k]; ok {
		v.Status = old.Status
		v.Message = old.Message
	}
	if v.Status == "" {
		v.Status = JobPending
	}
	v.Metadata = CloneMetadata(v.Metadata)
	c.Jobs[k] = v
	c.Log.Add("upsert job " + k)
	return nil
}
func (c *MemoryClient) GetJob(ctx context.Context, ns, name string) (Job, bool, error) {
	if err := ctx.Err(); err != nil {
		return Job{}, false, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.Jobs[Key(ns, name)]
	v.Metadata = CloneMetadata(v.Metadata)
	return v, ok, nil
}
func (c *MemoryClient) ListJobs(ctx context.Context, ns string, labels map[string]string) ([]Job, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	out := []Job{}
	for _, v := range c.Jobs {
		if v.Metadata.Namespace == ns && LabelsMatch(v.Metadata.Labels, labels) {
			v.Metadata = CloneMetadata(v.Metadata)
			out = append(out, v)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Metadata.Name < out[j].Metadata.Name })
	return out, nil
}
func (c *MemoryClient) DeleteJob(ctx context.Context, ns, name string) error {
	return c.delete(ctx, "job", Key(ns, name), func() { delete(c.Jobs, Key(ns, name)) })
}
func (c *MemoryClient) UpsertHTTPRoute(ctx context.Context, v HTTPRoute) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ensure()
	v.Metadata = CloneMetadata(v.Metadata)
	c.Routes[Key(v.Metadata.Namespace, v.Metadata.Name)] = v
	c.Log.Add("upsert route " + Key(v.Metadata.Namespace, v.Metadata.Name))
	return nil
}
func (c *MemoryClient) GetHTTPRoute(ctx context.Context, ns, name string) (HTTPRoute, bool, error) {
	if err := ctx.Err(); err != nil {
		return HTTPRoute{}, false, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.Routes[Key(ns, name)]
	v.Metadata = CloneMetadata(v.Metadata)
	return v, ok, nil
}
func (c *MemoryClient) DeleteHTTPRoute(ctx context.Context, ns, name string) error {
	return c.delete(ctx, "route", Key(ns, name), func() { delete(c.Routes, Key(ns, name)) })
}
func (c *MemoryClient) UpsertNetworkPolicy(ctx context.Context, v NetworkPolicy) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ensure()
	v.Metadata = CloneMetadata(v.Metadata)
	v.AllowedIngress = append([]string(nil), v.AllowedIngress...)
	v.AllowedEgress = append([]string(nil), v.AllowedEgress...)
	c.Policies[Key(v.Metadata.Namespace, v.Metadata.Name)] = v
	c.Log.Add("upsert networkpolicy " + Key(v.Metadata.Namespace, v.Metadata.Name))
	return nil
}
func (c *MemoryClient) GetNetworkPolicy(ctx context.Context, ns, name string) (NetworkPolicy, bool, error) {
	if err := ctx.Err(); err != nil {
		return NetworkPolicy{}, false, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.Policies[Key(ns, name)]
	v.Metadata = CloneMetadata(v.Metadata)
	return v, ok, nil
}
func (c *MemoryClient) DeleteNetworkPolicy(ctx context.Context, ns, name string) error {
	return c.delete(ctx, "networkpolicy", Key(ns, name), func() { delete(c.Policies, Key(ns, name)) })
}
func (c *MemoryClient) UpdatePaaSAppStatus(ctx context.Context, ns, name string, status runtimev1.PaaSAppStatus) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ensure()
	k := Key(ns, name)
	app := c.Apps[k]
	app.Status = status
	c.Apps[k] = app
	c.Log.Add("status paasapp " + k)
	return nil
}
func (c *MemoryClient) delete(ctx context.Context, kind, key string, immediate func()) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ensure()
	c.Log.Add("delete " + kind + " " + key)
	if c.DeferDeletion {
		c.pending[kind+"|"+key] = pendingDeletion{Kind: kind, Key: key}
	} else {
		immediate()
	}
	return nil
}

var _ Client = (*MemoryClient)(nil)
