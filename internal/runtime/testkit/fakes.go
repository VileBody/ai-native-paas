package testkit

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/keir-research/ai-native-paas/internal/runtime/application"
	buildv1 "github.com/keir-research/ai-native-paas/pkg/contracts/build/v1"
	runtimev1 "github.com/keir-research/ai-native-paas/pkg/contracts/runtime/v1"
)

type Clock struct {
	mu sync.Mutex
	T  time.Time
}

func (c *Clock) Now() time.Time          { c.mu.Lock(); defer c.mu.Unlock(); return c.T.UTC() }
func (c *Clock) Advance(d time.Duration) { c.mu.Lock(); defer c.mu.Unlock(); c.T = c.T.Add(d) }

type IDs struct {
	mu sync.Mutex
	n  int
}

func (i *IDs) New(prefix string) string {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.n++
	return fmt.Sprintf("%s-%d", prefix, i.n)
}

type ArtifactPolicy struct {
	mu            sync.Mutex
	Allowed       bool
	PolicyVersion string
	Reasons       []string
	Calls         int
	Decisions     map[string]bool
}

func (p *ArtifactPolicy) IsReleasable(_ context.Context, ref buildv1.ArtifactRef) (buildv1.ReleasabilityDecision, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.Calls++
	allowed := p.Allowed
	if p.Decisions != nil {
		if value, ok := p.Decisions[ref.Digest]; ok {
			allowed = value
		}
	}
	version := p.PolicyVersion
	if version == "" {
		version = "policy-v1"
	}
	reasons := append([]string(nil), p.Reasons...)
	if !allowed && len(reasons) == 0 {
		reasons = []string{"policy rejected artifact"}
	}
	return buildv1.ReleasabilityDecision{Allowed: allowed, PolicyVersion: version, Reasons: reasons}, nil
}
func (p *ArtifactPolicy) CallCount() int { p.mu.Lock(); defer p.mu.Unlock(); return p.Calls }

type Observer struct {
	mu      sync.Mutex
	Objects map[string]runtimev1.PaaSApp
	Error   error
}

func (o *Observer) GetPaaSApp(_ context.Context, ref application.RuntimeObjectRef) (runtimev1.PaaSApp, bool, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.Error != nil {
		return runtimev1.PaaSApp{}, false, o.Error
	}
	v, ok := o.Objects[ref.CellID+"/"+ref.Namespace+"/"+ref.Name]
	return v, ok, nil
}
func (o *Observer) Put(cell string, app runtimev1.PaaSApp) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.Objects == nil {
		o.Objects = map[string]runtimev1.PaaSApp{}
	}
	o.Objects[cell+"/"+app.Metadata.Namespace+"/"+app.Metadata.Name] = app
}

func Artifact(seed string) buildv1.ArtifactRef {
	if seed == "" {
		seed = "a"
	}
	ch := seed[0]
	hex := "0123456789abcdef"
	if !contains(hex, ch) {
		ch = 'a'
	}
	return buildv1.ArtifactRef{ArtifactID: "art-" + string(ch), Repository: "registry.test/tenants/tenant-1/apps/app-1", Digest: "sha256:" + repeat(string(ch), 64), MediaType: "application/vnd.oci.image.manifest.v1+json"}
}
func Config() runtimev1.ReleaseConfig {
	return runtimev1.ReleaseConfig{Region: "eu1", Isolation: runtimev1.IsolationSandboxed, Unit: "u1", Processes: map[string]runtimev1.ProcessSpec{"web": {Port: 8080, MinReplicas: 1, MaxReplicas: 3, HealthPath: "/health", StartupTimeout: 60, ReadinessTimeout: 30}}, AttachmentSnapshotRef: "none", RolloutTimeoutSeconds: 300, EgressProfile: "public-default"}
}
func repeat(v string, n int) string {
	out := ""
	for len(out) < n {
		out += v
	}
	return out[:n]
}
func contains(v string, ch byte) bool {
	for i := 0; i < len(v); i++ {
		if v[i] == ch {
			return true
		}
	}
	return false
}
