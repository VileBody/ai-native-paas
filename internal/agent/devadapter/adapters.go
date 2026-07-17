package devadapter

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	kernelv1 "github.com/keir-research/ai-native-paas/contracts/kernel/v1"
	"github.com/keir-research/ai-native-paas/internal/agent/application"
	attachmentsv1 "github.com/keir-research/ai-native-paas/pkg/contracts/attachments/v1"
	buildv1 "github.com/keir-research/ai-native-paas/pkg/contracts/build/v1"
	commercev1 "github.com/keir-research/ai-native-paas/pkg/contracts/commerce/v1"
	runtimev1 "github.com/keir-research/ai-native-paas/pkg/contracts/runtime/v1"
	sourcev1 "github.com/keir-research/ai-native-paas/pkg/contracts/source/v1"
	"sync"
	"sync/atomic"
	"time"
)

type Clock struct {
	mu sync.Mutex
	T  time.Time
}

func (c *Clock) Now() time.Time          { c.mu.Lock(); defer c.mu.Unlock(); return c.T }
func (c *Clock) Advance(d time.Duration) { c.mu.Lock(); c.T = c.T.Add(d); c.mu.Unlock() }

type IDs struct{ n atomic.Int64 }

func (i *IDs) NewID(prefix string) string { return fmt.Sprintf("%s-%06d", prefix, i.n.Add(1)) }

type Source struct {
	mu                                      sync.Mutex
	Projects                                map[string]application.ProjectRef
	ByKey                                   map[string]string
	CreateCalls, PatchCalls, ReconcileCalls int
	LostWebhook                             bool
	Credential                              string
	Fail                                    error
}

func NewSource() *Source {
	return &Source{Projects: map[string]application.ProjectRef{}, ByKey: map[string]string{}}
}
func (s *Source) CreateProject(_ context.Context, tenant, actor, name, key string) (application.ProjectRef, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.CreateCalls++
	if s.Fail != nil {
		return application.ProjectRef{}, s.Fail
	}
	if id := s.ByKey[tenant+":"+key]; id != "" {
		return s.Projects[id], nil
	}
	id := fmt.Sprintf("project-%d", len(s.Projects)+1)
	v := application.ProjectRef{ProjectID: id, RepositoryID: "repo-" + id, WebURL: "https://git.invalid/" + id, State: "READY"}
	s.Projects[id] = v
	s.ByKey[tenant+":"+key] = id
	return v, nil
}
func (s *Source) GetProject(_ context.Context, tenant, id string) (application.ProjectRef, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.Projects[id]
	if !ok {
		return v, fmt.Errorf("not found")
	}
	return v, nil
}
func (s *Source) ApplyPatch(_ context.Context, tenant, actor string, a application.ApplyPatchArguments, key string) (application.CommitRef, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.PatchCalls++
	if s.Fail != nil {
		return application.CommitRef{}, s.Fail
	}
	return application.CommitRef{ProjectID: a.ProjectID, RepositoryID: "repo-" + a.ProjectID, Branch: a.Branch, CommitSHA: "abcdef0123456789"}, nil
}
func (s *Source) CreateBranch(_ context.Context, tenant, actor string, a application.CreateBranchArguments, key string) (application.CommitRef, error) {
	return application.CommitRef{ProjectID: a.ProjectID, RepositoryID: "repo-" + a.ProjectID, Branch: a.Branch, CommitSHA: a.BaseCommitSHA}, nil
}
func (s *Source) CreateMergeRequest(_ context.Context, tenant, actor string, a application.CreateMergeRequestArguments, key string) (application.MergeRequestRef, error) {
	return application.MergeRequestRef{ProjectID: a.ProjectID, MergeRequestID: "mr-1", URL: "https://git.invalid/mr/1", State: "OPEN"}, nil
}
func (s *Source) Reconcile(context.Context, string, string) error {
	s.mu.Lock()
	s.ReconcileCalls++
	s.LostWebhook = false
	s.mu.Unlock()
	return nil
}

type Builds struct {
	mu                        sync.Mutex
	ByID                      map[string]buildv1.BuildView
	ByKey                     map[string]string
	RequestCalls, ResumeCalls int
	TimeoutOnce               bool
	Fail                      error
}

func NewBuilds() *Builds {
	return &Builds{ByID: map[string]buildv1.BuildView{}, ByKey: map[string]string{}}
}
func (b *Builds) Request(_ context.Context, tenant string, rev sourcev1.SourceRevision, minutes int64, key, corr string) (application.BuildResult, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.RequestCalls++
	if b.Fail != nil {
		return application.BuildResult{}, b.Fail
	}
	if id := b.ByKey[tenant+":"+key]; id != "" {
		return application.BuildResult{Build: b.ByID[id]}, nil
	}
	id := fmt.Sprintf("build-%d", len(b.ByID)+1)
	artifact := buildv1.ArtifactRef{ArtifactID: "artifact-" + id, Repository: "registry.invalid/" + tenant + "/app", Digest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", MediaType: "application/vnd.oci.image.manifest.v1+json"}
	v := buildv1.BuildView{BuildID: id, TenantID: tenant, Identity: rev.CommitSHA, State: buildv1.BuildSucceeded, CorrelationID: corr, Artifact: &artifact, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	b.ByID[id] = v
	b.ByKey[tenant+":"+key] = id
	if b.TimeoutOnce {
		b.TimeoutOnce = false
		return application.BuildResult{}, &application.ProviderError{Message: "response lost token=supersecret", Retryable: true, OperationID: "op-" + id}
	}
	return application.BuildResult{Build: v}, nil
}
func (b *Builds) Get(_ context.Context, tenant, id string) (application.BuildResult, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	v, ok := b.ByID[id]
	if !ok {
		return application.BuildResult{}, fmt.Errorf("build not found")
	}
	return application.BuildResult{Build: v}, nil
}
func (b *Builds) Resume(_ context.Context, tenant, op string) (application.BuildResult, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.ResumeCalls++
	id := op[len("op-"):]
	v, ok := b.ByID[id]
	if !ok {
		return application.BuildResult{}, fmt.Errorf("operation not found")
	}
	return application.BuildResult{Build: v}, nil
}

type Runtime struct {
	mu                          sync.Mutex
	ByID                        map[string]runtimev1.RuntimeStatus
	ByKey                       map[string]string
	DeployCalls, ReconcileCalls int
	LastDeployRequest           runtimev1.DeployRequest
	LastExpectedRevision        int64
	TimeoutOnce, StatusLost     bool
	Fail                        error
}

func NewRuntime() *Runtime {
	return &Runtime{ByID: map[string]runtimev1.RuntimeStatus{}, ByKey: map[string]string{}}
}
func (r *Runtime) Deploy(_ context.Context, req runtimev1.DeployRequest, expected int64) (application.RuntimeResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.DeployCalls++
	r.LastDeployRequest = req
	r.LastExpectedRevision = expected
	if r.Fail != nil {
		return application.RuntimeResult{}, r.Fail
	}
	if id := r.ByKey[req.TenantID+":"+req.IdempotencyKey]; id != "" {
		st := r.ByID[id]
		return application.RuntimeResult{Deployment: runtimev1.DeploymentRef{DeploymentID: id, ReleaseID: st.ActiveRelease, Phase: st.Phase, GitOpsRevision: st.GitOpsRevision}, Status: &st}, nil
	}
	id := fmt.Sprintf("deployment-%d", len(r.ByID)+1)
	revisionHash := sha256.Sum256([]byte(req.Artifact.Digest))
	revision := hex.EncodeToString(revisionHash[:])[:40]
	st := runtimev1.RuntimeStatus{DeploymentID: id, Phase: runtimev1.DeploymentReady, ActiveRelease: "release-" + id, GitOpsRevision: revision, URL: runtimeURL(req), ReadyReplicas: 1}
	r.ByID[id] = st
	r.ByKey[req.TenantID+":"+req.IdempotencyKey] = id
	if r.TimeoutOnce {
		r.TimeoutOnce = false
		return application.RuntimeResult{}, &application.ProviderError{Message: "gitops response lost", Retryable: true, OperationID: "op-" + id}
	}
	return application.RuntimeResult{Deployment: runtimev1.DeploymentRef{DeploymentID: id, ReleaseID: st.ActiveRelease, Phase: st.Phase, GitOpsRevision: st.GitOpsRevision}, Status: &st}, nil
}
func runtimeURL(req runtimev1.DeployRequest) string {
	if req.Configuration.GeneratedHostname == "" {
		return "https://app.invalid"
	}
	return "https://" + req.Configuration.GeneratedHostname
}
func (r *Runtime) Get(_ context.Context, tenant, id string) (application.RuntimeResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.StatusLost {
		r.StatusLost = false
		return application.RuntimeResult{}, &application.ProviderError{Message: "status lost", Retryable: true}
	}
	st, ok := r.ByID[id]
	if !ok {
		return application.RuntimeResult{}, fmt.Errorf("deployment not found")
	}
	return application.RuntimeResult{Deployment: runtimev1.DeploymentRef{DeploymentID: id, ReleaseID: st.ActiveRelease, Phase: st.Phase, GitOpsRevision: st.GitOpsRevision}, Status: &st}, nil
}
func (r *Runtime) Rollback(_ context.Context, tenant, deployment, release string, expected int64, key string) (application.RuntimeResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	st, ok := r.ByID[deployment]
	if !ok {
		return application.RuntimeResult{}, fmt.Errorf("deployment not found")
	}
	st.ActiveRelease = release
	r.ByID[deployment] = st
	return application.RuntimeResult{Deployment: runtimev1.DeploymentRef{DeploymentID: deployment, ReleaseID: release, Phase: st.Phase, GitOpsRevision: st.GitOpsRevision}, Status: &st}, nil
}
func (r *Runtime) Reconcile(context.Context, string, string) error {
	r.mu.Lock()
	r.ReconcileCalls++
	r.mu.Unlock()
	return nil
}

type Attachments struct {
	mu       sync.Mutex
	Secrets  map[string]string
	Metadata map[string]attachmentsv1.SecretMetadata
	SetCalls int
	Fail     error
}

func NewAttachments() *Attachments {
	return &Attachments{Secrets: map[string]string{}, Metadata: map[string]attachmentsv1.SecretMetadata{}}
}
func (a *Attachments) SetSecret(_ context.Context, r attachmentsv1.SetSecretRequest) (attachmentsv1.SecretMetadata, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.SetCalls++
	if a.Fail != nil {
		return attachmentsv1.SecretMetadata{}, a.Fail
	}
	key := r.TenantID + ":" + r.ApplicationID + ":" + r.EnvironmentID + ":" + r.Name
	a.Secrets[key] = r.Value
	m := attachmentsv1.SecretMetadata{Name: r.Name, Version: 1, UpdatedAt: time.Now().UTC()}
	a.Metadata[key] = m
	return m, nil
}
func (a *Attachments) ListSecretMetadata(_ context.Context, t, app, env string) ([]attachmentsv1.SecretMetadata, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := []attachmentsv1.SecretMetadata{}
	prefix := t + ":" + app + ":" + env + ":"
	for k, v := range a.Metadata {
		if len(k) >= len(prefix) && k[:len(prefix)] == prefix {
			out = append(out, v)
		}
	}
	return out, nil
}
func (a *Attachments) Provision(_ context.Context, r attachmentsv1.ServiceRequest) (attachmentsv1.ServiceInstanceRef, error) {
	if a.Fail != nil {
		return attachmentsv1.ServiceInstanceRef{}, a.Fail
	}
	return attachmentsv1.ServiceInstanceRef{ServiceInstanceID: "svc-" + r.Name, State: "READY"}, nil
}
func (a *Attachments) Bind(_ context.Context, r attachmentsv1.BindRequest) (attachmentsv1.BindingRef, error) {
	if a.Fail != nil {
		return attachmentsv1.BindingRef{}, a.Fail
	}
	return attachmentsv1.BindingRef{BindingID: "binding-1", SnapshotID: "snapshot-1", State: "READY"}, nil
}
func (a *Attachments) AddDomain(_ context.Context, r attachmentsv1.DomainRequest) (attachmentsv1.DomainRef, error) {
	if a.Fail != nil {
		return attachmentsv1.DomainRef{}, a.Fail
	}
	return attachmentsv1.DomainRef{DomainClaimID: "domain-1", Hostname: r.Hostname, State: "VERIFYING"}, nil
}

type Commerce struct {
	mu      sync.Mutex
	Allowed bool
	Reason  string
	Calls   int
}

func (c *Commerce) Check(context.Context, commercev1.EntitlementRequest) (commercev1.EntitlementDecision, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.Calls++
	if !c.Allowed {
		return commercev1.EntitlementDecision{Allowed: false, Reason: c.Reason, PolicyVersion: "policy-v1"}, nil
	}
	return commercev1.EntitlementDecision{Allowed: true, PolicyVersion: "policy-v1", PlanVersionID: "plan-v1"}, nil
}

type Operations struct {
	mu       sync.Mutex
	ByID     map[string]kernelv1.OperationSnapshot
	Canceled map[string]bool
}

func NewOperations() *Operations {
	return &Operations{ByID: map[string]kernelv1.OperationSnapshot{}, Canceled: map[string]bool{}}
}
func (o *Operations) Get(_ context.Context, tenant, id string) (kernelv1.OperationSnapshot, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	v, ok := o.ByID[id]
	if !ok {
		return v, fmt.Errorf("operation not found")
	}
	return v, nil
}
func (o *Operations) Cancel(_ context.Context, tenant, id string) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.Canceled[id] = true
	return nil
}

type Logs struct{ Lines []string }

func (l *Logs) GetLogs(context.Context, string, string, int) ([]string, error) {
	return append([]string(nil), l.Lines...), nil
}

type Usage struct{ Preview commercev1.InvoicePreview }

func (u *Usage) GetUsage(context.Context, string, string) (commercev1.InvoicePreview, error) {
	return u.Preview, nil
}
