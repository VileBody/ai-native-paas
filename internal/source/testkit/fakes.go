package testkit

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/keir-research/ai-native-paas/internal/source/application"
)

type Clock struct {
	mu sync.Mutex
	T  time.Time
}

func (c *Clock) Now() time.Time          { c.mu.Lock(); defer c.mu.Unlock(); return c.T }
func (c *Clock) Advance(d time.Duration) { c.mu.Lock(); c.T = c.T.Add(d); c.mu.Unlock() }

type IDs struct {
	mu sync.Mutex
	N  int
}

func (i *IDs) NewID(prefix string) string {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.N++
	return fmt.Sprintf("%s-%04d", prefix, i.N)
}

type Provider struct {
	mu                                                      sync.Mutex
	NextID                                                  int64
	Repositories                                            map[int64]application.ProviderRepository
	Correlations                                            map[string]int64
	Heads                                                   map[string]string
	CreateCalls, ProtectCalls, CredentialCalls, RevokeCalls int
	LostResponseOnce                                        bool
	ProtectError                                            error
	RevokeError                                             error
	Token                                                   string
}

func NewProvider() *Provider {
	return &Provider{NextID: 100, Repositories: map[int64]application.ProviderRepository{}, Correlations: map[string]int64{}, Heads: map[string]string{}, Token: "super-secret-token"}
}
func key(id int64, branch string) string { return fmt.Sprintf("%d:%s", id, branch) }
func (p *Provider) CreateRepository(_ context.Context, r application.CreateRepositoryRequest) (application.ProviderRepository, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.CreateCalls++
	if id, ok := p.Correlations[r.CorrelationID]; ok {
		return p.Repositories[id], nil
	}
	p.NextID++
	repo := application.ProviderRepository{ID: p.NextID, NamespaceID: r.NamespaceID, Path: r.Path, PathWithNamespace: fmt.Sprintf("group-%d/%s", r.NamespaceID, r.Path), WebURL: "https://git.example/" + r.Path, DefaultBranch: r.DefaultBranch, Description: "[paas-correlation:" + r.CorrelationID + "]"}
	p.Repositories[repo.ID] = repo
	p.Correlations[r.CorrelationID] = repo.ID
	p.Heads[key(repo.ID, r.DefaultBranch)] = strings40("a")
	if p.LostResponseOnce {
		p.LostResponseOnce = false
		return application.ProviderRepository{}, errors.New("lost response after create")
	}
	return repo, nil
}
func (p *Provider) FindRepositoryByCorrelation(_ context.Context, _ int64, corr string) (application.ProviderRepository, bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	id, ok := p.Correlations[corr]
	return p.Repositories[id], ok, nil
}
func (p *Provider) GetRepository(_ context.Context, id int64) (application.ProviderRepository, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	r, ok := p.Repositories[id]
	if !ok {
		return r, errors.New("not found")
	}
	return r, nil
}
func (p *Provider) ProtectBranch(_ context.Context, _ int64, _ string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.ProtectCalls++
	return p.ProtectError
}
func (p *Provider) GetBranchHead(_ context.Context, id int64, branch string) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	v, ok := p.Heads[key(id, branch)]
	if !ok {
		return "", errors.New("branch not found")
	}
	return v, nil
}
func (p *Provider) CreateCredential(_ context.Context, _ int64, _ string, expires time.Time) (application.ProviderCredential, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.CredentialCalls++
	return application.ProviderCredential{ID: fmt.Sprintf("cred-%d", p.CredentialCalls), Username: "oauth2", Token: p.Token, ExpiresAt: expires}, nil
}
func (p *Provider) RevokeCredential(_ context.Context, _ int64, _ string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.RevokeCalls++
	if p.RevokeError != nil {
		return p.RevokeError
	}
	return nil
}
func (p *Provider) CreateMergeRequest(_ context.Context, r application.CreateMergeRequestRequest) (application.ProviderMergeRequest, error) {
	return application.ProviderMergeRequest{IID: 1, State: "opened", SourceBranch: r.SourceBranch, TargetBranch: r.TargetBranch, HeadSHA: strings40("b")}, nil
}
func (p *Provider) SetHead(id int64, branch, sha string) {
	p.mu.Lock()
	p.Heads[key(id, branch)] = sha
	p.mu.Unlock()
}
func (p *Provider) Rename(id int64, path string) {
	p.mu.Lock()
	r := p.Repositories[id]
	r.Path = path
	r.PathWithNamespace = "renamed/" + path
	r.WebURL = "https://git.example/renamed/" + path
	p.Repositories[id] = r
	p.mu.Unlock()
}
func (p *Provider) Seed(id, namespace int64, path, branch, head string) application.ProviderRepository {
	p.mu.Lock()
	defer p.mu.Unlock()
	r := application.ProviderRepository{ID: id, NamespaceID: namespace, Path: path, PathWithNamespace: "group/" + path, WebURL: "https://git.example/group/" + path, DefaultBranch: branch}
	p.Repositories[id] = r
	p.Heads[key(id, branch)] = head
	return r
}
func strings40(c string) string {
	v := ""
	for len(v) < 40 {
		v += c
	}
	return v[:40]
}

type Git struct {
	mu                      sync.Mutex
	Dir                     string
	SHA                     string
	PushCalls, CleanupCalls int
	PushError               error
	RemoteSHA               string
}

func (g *Git) CloneExact(context.Context, string, string, string, application.ProviderCredential) (string, error) {
	if g.Dir == "" {
		g.Dir = "/tmp/fake-workspace"
	}
	return g.Dir, nil
}
func (g *Git) Apply(context.Context, string, []application.PatchOperation) error { return nil }
func (g *Git) Commit(context.Context, string, string, string, string) (string, error) {
	if g.SHA == "" {
		g.SHA = strings40("c")
	}
	return g.SHA, nil
}
func (g *Git) Push(context.Context, string, string, string, string, application.ProviderCredential) (bool, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.PushCalls++
	if g.PushError != nil {
		return false, g.PushError
	}
	g.RemoteSHA = g.SHA
	return true, nil
}
func (g *Git) RemoteHead(context.Context, string, string, application.ProviderCredential) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.RemoteSHA, nil
}
func (g *Git) Cleanup(context.Context, string) error {
	g.mu.Lock()
	g.CleanupCalls++
	g.mu.Unlock()
	return nil
}
