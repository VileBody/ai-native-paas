package workspace

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	workspacev1 "github.com/keir-research/ai-native-paas/pkg/contracts/workspace/v1"
)

type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) Add(value time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(value)
	c.mu.Unlock()
}

type testIDs struct {
	mu sync.Mutex
	n  int
}

func (i *testIDs) New(prefix string) string {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.n++
	return fmt.Sprintf("%s-%d", prefix, i.n)
}

type fakeProvider struct {
	mu                 sync.Mutex
	requests           []ProviderCreateRequest
	vm                 ProviderVM
	createLostResponse bool
	destroyed          bool
}

func (p *fakeProvider) FindByCorrelation(_ context.Context, correlationID string) (ProviderVM, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.vm.CorrelationID != correlationID || p.destroyed {
		return ProviderVM{}, ErrNotFound
	}
	return p.vm, nil
}

func (p *fakeProvider) Create(_ context.Context, request ProviderCreateRequest) (ProviderVM, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.requests = append(p.requests, request)
	p.vm = ProviderVM{
		VMID: "twc-vm-1", DiskIDs: []string{"twc-disk-1"}, FirewallGroupIDs: []string{"twc-firewall-1"}, CorrelationID: request.CorrelationID,
		ImageDigest: request.ImageDigest, NetworkProfile: request.NetworkProfile,
		PrivateAddressOnly: request.NetworkIsolation.PrivateAddressOnly,
		DenyAllInbound:     request.NetworkIsolation.DenyAllInbound, OutboundAgentReady: true,
	}
	if p.createLostResponse {
		p.createLostResponse = false
		return ProviderVM{}, errors.New("provider response lost")
	}
	return p.vm, nil
}

func (p *fakeProvider) Destroy(_ context.Context, vm ProviderVM) (DestroyEvidence, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if vm.CorrelationID != p.vm.CorrelationID {
		return DestroyEvidence{}, errors.New("wrong workspace provider identity")
	}
	p.destroyed = true
	return DestroyEvidence{VMAbsent: true, AbsentDiskIDs: append([]string(nil), p.vm.DiskIDs...), AbsentFirewallGroupIDs: append([]string(nil), p.vm.FirewallGroupIDs...)}, nil
}

type fakeSessions struct {
	mu         sync.Mutex
	connected  bool
	vmID       string
	dispatched map[string]int
	closed     []string
	canceled   []string
}

func (s *fakeSessions) Connected(_ context.Context, _, vmID string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.connected && vmID == s.vmID, nil
}

func (s *fakeSessions) Dispatch(_ context.Context, envelope CommandEnvelope) (DispatchReceipt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.dispatched == nil {
		s.dispatched = make(map[string]int)
	}
	s.dispatched[envelope.CommandID]++
	return DispatchReceipt{CommandID: envelope.CommandID, WorkspaceID: envelope.WorkspaceID, VMID: s.vmID, AgentSessionID: "mtls-session-1", Accepted: true}, nil
}

func (s *fakeSessions) RequestCancel(_ context.Context, workspaceID, commandID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.canceled = append(s.canceled, workspaceID+"/"+commandID)
	return nil
}

func (s *fakeSessions) Close(_ context.Context, workspaceID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = append(s.closed, workspaceID)
	return nil
}

type fakeLeases struct {
	mu      sync.Mutex
	revoked map[string]int
}

type fakeCredentials struct {
	request CredentialSourceRequest
}

type fakeOutputs struct {
	scope CommandOutputScope
	chunk workspacev1.AgentOutputChunk
}

func (f *fakeOutputs) PutChunk(_ context.Context, scope CommandOutputScope, chunk workspacev1.AgentOutputChunk) error {
	f.scope, f.chunk = scope, chunk
	return nil
}

func (f *fakeCredentials) Resolve(_ context.Context, request CredentialSourceRequest) (workspacev1.AgentCredentialView, error) {
	f.request = request
	return workspacev1.AgentCredentialView{Values: map[string]string{"GITLAB_TOKEN": "short-lived-token"}, ExpiresAt: time.Date(2026, 7, 14, 6, 10, 0, 0, time.UTC)}, nil
}

func (l *fakeLeases) Revoke(_ context.Context, leaseID, _ string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.revoked == nil {
		l.revoked = make(map[string]int)
	}
	l.revoked[leaseID]++
	return nil
}

type fixture struct {
	service     *Service
	store       *MemoryStore
	provider    *fakeProvider
	sessions    *fakeSessions
	leases      *fakeLeases
	credentials *fakeCredentials
	outputs     *fakeOutputs
	clock       *testClock
	scope       Scope
	spec        workspacev1.WorkspaceSpec
}

func newFixture() *fixture {
	clock := &testClock{now: time.Date(2026, 7, 14, 6, 0, 0, 0, time.UTC)}
	store := NewMemoryStore()
	provider := &fakeProvider{}
	sessions := &fakeSessions{connected: true, vmID: "twc-vm-1"}
	leases := &fakeLeases{}
	credentials := &fakeCredentials{}
	outputs := &fakeOutputs{}
	service := &Service{
		Store: store, Provider: provider, Sessions: sessions, Leases: leases, Credentials: credentials, Outputs: outputs, Clock: clock, IDs: &testIDs{},
		Policy: DefaultCommandPolicy(), ReconcilerID: "workspace-manager-1", WorkspaceVPCID: "vpc-workspace",
		AllowedEgressHosts: []string{"gitlab.com", "registry.npmjs.org", "ai-native-paas-registry.registry.twcstorage.ru"},
		EgressGatewayCIDRs: []string{"192.168.75.4/32"}, DNSResolverCIDRs: []string{"192.168.75.1/32"},
		DeniedCIDRs: []string{"192.168.73.0/24", "192.168.74.0/24", "10.0.0.0/8", "172.16.0.0/12"},
	}
	return &fixture{
		service: service, store: store, provider: provider, sessions: sessions, leases: leases, credentials: credentials, outputs: outputs, clock: clock,
		scope: Scope{TenantID: "tenant-1", ProjectID: "project-1", ActorID: "agent-1"},
		spec: workspacev1.WorkspaceSpec{
			ProjectID: "project-1", TaskID: "task-1", ImageDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			CPUMillis: 2000, MemoryMiB: 4096, TTLSeconds: 900, NetworkProfile: "isolated-governed", CredentialLeases: []string{"repo-lease", "command-lease"},
		},
	}
}

func TestWorkspace_CredentialsUsePersistedCommandAndMTLSExecutionBinding(t *testing.T) {
	f := newFixture()
	ready := f.ready(t)
	view, err := f.service.Exec(context.Background(), ExecRequest{
		Scope: f.scope, WorkspaceID: ready.WorkspaceID, Kind: "git_checkout", IdempotencyKey: "credentials-1",
		CredentialLeases: []string{"repo-lease"}, Spec: workspacev1.CommandSpec{
			Argv: []string{"git", "fetch", "origin"}, WorkingDir: "repo", TimeoutSeconds: 60, OutputLimitBytes: 4096,
			EnvironmentRefs: map[string]string{"GITLAB_TOKEN": "credential://repo-lease"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	view, err = f.service.Dispatch(context.Background(), f.scope, view.CommandID)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := f.service.ResolveCredentials(context.Background(), CredentialResolveRequest{
		TenantID: f.scope.TenantID, ProjectID: f.scope.ProjectID, WorkspaceID: ready.WorkspaceID, TaskID: "task-1",
		CommandID: view.CommandID, AgentSessionID: "mtls-session-1", VMID: "twc-vm-1",
	})
	if err != nil || resolved.Values["GITLAB_TOKEN"] != "short-lived-token" || f.credentials.request.EnvironmentRefs["GITLAB_TOKEN"] != "credential://repo-lease" {
		t.Fatalf("resolved=%#v source=%#v err=%v", resolved, f.credentials.request, err)
	}
	if _, err := f.service.ResolveCredentials(context.Background(), CredentialResolveRequest{
		TenantID: f.scope.TenantID, ProjectID: f.scope.ProjectID, WorkspaceID: ready.WorkspaceID, TaskID: "task-1",
		CommandID: view.CommandID, AgentSessionID: "mtls-session-1", VMID: "attacker-vm",
	}); !errors.Is(err, ErrPolicyDenied) {
		t.Fatalf("foreign execution binding accepted: %v", err)
	}
	chunk := workspacev1.AgentOutputChunk{
		SessionID: "mtls-session-1", ExecutionSessionID: "mtls-session-1", CommandID: view.CommandID,
		Stream: workspacev1.AgentOutputStdout, Sequence: 0, Data: []byte("safe"),
		ChunkSHA256: "sha256:8b3369944dd2a3fab39e32d1aeb1f763946a458ae3e6368a46432adc8f3a0860", Final: true,
		TotalSHA256: "sha256:8b3369944dd2a3fab39e32d1aeb1f763946a458ae3e6368a46432adc8f3a0860",
	}
	if err := f.service.RecordOutputChunk(context.Background(), CredentialResolveRequest{
		TenantID: f.scope.TenantID, ProjectID: f.scope.ProjectID, WorkspaceID: ready.WorkspaceID, TaskID: "task-1",
		CommandID: view.CommandID, AgentSessionID: "mtls-session-1", VMID: "twc-vm-1",
	}, chunk); err != nil || f.outputs.scope.CommandID != view.CommandID || f.outputs.chunk.SessionID != "mtls-session-1" {
		t.Fatalf("output scope=%#v chunk=%#v err=%v", f.outputs.scope, f.outputs.chunk, err)
	}
}

func (f *fixture) ready(t *testing.T) workspacev1.WorkspaceRef {
	t.Helper()
	created, err := f.service.Create(context.Background(), CreateRequest{Scope: f.scope, Spec: f.spec, IdempotencyKey: "create-1"})
	if err != nil {
		t.Fatal(err)
	}
	ready, err := f.service.Reconcile(context.Background(), created.WorkspaceID)
	if err != nil {
		t.Fatal(err)
	}
	if ready.State != workspacev1.WorkspaceReady {
		t.Fatalf("workspace not ready: %#v", ready)
	}
	return ready
}

func TestWorkspace_CreateUsesPinnedImageDigestAndPolicyProfile(t *testing.T) {
	f := newFixture()
	created, err := f.service.Create(context.Background(), CreateRequest{Scope: f.scope, Spec: f.spec, IdempotencyKey: "create-1"})
	if err != nil || created.State != workspacev1.WorkspaceProvisioning || len(f.provider.requests) != 0 {
		t.Fatalf("create must only persist asynchronous intent: ref=%#v requests=%d err=%v", created, len(f.provider.requests), err)
	}
	ready, err := f.service.Reconcile(context.Background(), created.WorkspaceID)
	if err != nil || ready.State != workspacev1.WorkspaceReady || len(f.provider.requests) != 1 {
		t.Fatalf("reconcile failed: ref=%#v requests=%d err=%v", ready, len(f.provider.requests), err)
	}
	request := f.provider.requests[0]
	if request.ImageDigest != f.spec.ImageDigest || request.CPUMillis != f.spec.CPUMillis || request.MemoryMiB != f.spec.MemoryMiB || request.NetworkProfile != f.spec.NetworkProfile || !request.ExpiresAt.Equal(created.ExpiresAt) {
		t.Fatalf("provider request lost exact policy: %#v", request)
	}
	bad := f.spec
	bad.TaskID = "task-2"
	bad.ImageDigest = "workspace:latest"
	if _, err := f.service.Create(context.Background(), CreateRequest{Scope: f.scope, Spec: bad, IdempotencyKey: "create-2"}); err == nil {
		t.Fatal("mutable workspace image tag accepted")
	}
}

func TestWorkspace_RequestPlanePersistsIntentWithoutProviderAuthority(t *testing.T) {
	clock := &testClock{now: time.Date(2026, 7, 14, 6, 5, 0, 0, time.UTC)}
	requestPlane := &Service{Store: NewMemoryStore(), Clock: clock, IDs: &testIDs{}, Policy: DefaultCommandPolicy()}
	scope := Scope{TenantID: "tenant-request", ProjectID: "project-request", ActorID: "agent-request"}
	spec := workspacev1.WorkspaceSpec{
		ProjectID: "project-request", TaskID: "task-request",
		ImageDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		CPUMillis:   2000, MemoryMiB: 4096, TTLSeconds: 900, NetworkProfile: "isolated-governed",
	}
	created, err := requestPlane.Create(context.Background(), CreateRequest{Scope: scope, Spec: spec, IdempotencyKey: "request-only-create"})
	if err != nil || created.State != workspacev1.WorkspaceProvisioning {
		t.Fatalf("request plane did not persist intent: ref=%#v err=%v", created, err)
	}
	if _, err := requestPlane.Get(context.Background(), scope, created.WorkspaceID); err != nil {
		t.Fatal(err)
	}
	if _, err := requestPlane.Reconcile(context.Background(), created.WorkspaceID); err == nil {
		t.Fatal("request plane acquired provider reconciliation authority")
	}
}

func TestWorkspace_IsEphemeralAndDestroyRemovesDiskAndCredentials(t *testing.T) {
	f := newFixture()
	ready := f.ready(t)
	destroying, err := f.service.Destroy(context.Background(), f.scope, ready.WorkspaceID)
	if err != nil || destroying.State != workspacev1.WorkspaceDestroying {
		t.Fatalf("destroy intent failed: ref=%#v err=%v", destroying, err)
	}
	destroyed, err := f.service.Reconcile(context.Background(), ready.WorkspaceID)
	if err != nil || destroyed.State != workspacev1.WorkspaceDestroyed || !f.provider.destroyed || f.leases.revoked["repo-lease"] != 1 || len(f.sessions.closed) != 1 {
		t.Fatalf("destruction incomplete: ref=%#v provider=%v leases=%v sessions=%v err=%v", destroyed, f.provider.destroyed, f.leases.revoked, f.sessions.closed, err)
	}
}

func TestWorkspace_ProviderIdentityPersistsBeforeAgentCorrelationBinding(t *testing.T) {
	f := newFixture()
	f.sessions.connected = false
	created, err := f.service.Create(context.Background(), CreateRequest{Scope: f.scope, Spec: f.spec, IdempotencyKey: "create-1"})
	if err != nil {
		t.Fatal(err)
	}
	pending, err := f.service.Reconcile(context.Background(), created.WorkspaceID)
	if err != nil || pending.State != workspacev1.WorkspaceProvisioning {
		t.Fatalf("provider identity persistence reconcile=%#v err=%v", pending, err)
	}
	stored, err := f.store.GetWorkspace(context.Background(), f.scope.TenantID, f.scope.ProjectID, created.WorkspaceID)
	if err != nil || stored.ProviderVMID != "twc-vm-1" || len(stored.ProviderDiskIDs) != 1 || len(stored.ProviderFirewallGroupIDs) != 1 {
		t.Fatalf("provider identity was not persisted before agent connect: %#v err=%v", stored, err)
	}
	vmID, err := f.service.ResolveAgentBinding(context.Background(), stored.TenantID, stored.ProjectID, stored.ID, stored.TaskID, stored.CorrelationID)
	if err != nil || vmID != stored.ProviderVMID {
		t.Fatalf("correlation binding vm=%q err=%v", vmID, err)
	}
	if _, err := f.service.ResolveAgentBinding(context.Background(), stored.TenantID, stored.ProjectID, stored.ID, stored.TaskID, "foreign-correlation"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign correlation binding err=%v", err)
	}
	f.sessions.connected = true
	ready, err := f.service.Reconcile(context.Background(), created.WorkspaceID)
	if err != nil || ready.State != workspacev1.WorkspaceReady || len(f.provider.requests) != 1 {
		t.Fatalf("agent connect did not complete readiness: ref=%#v creates=%d err=%v", ready, len(f.provider.requests), err)
	}
}

func TestWorkspace_CommandRunsOnlyInsideWorkspaceNotControlPlaneHost(t *testing.T) {
	f := newFixture()
	ready := f.ready(t)
	view, err := f.service.Exec(context.Background(), ExecRequest{
		Scope: f.scope, WorkspaceID: ready.WorkspaceID, Kind: "repository_status", IdempotencyKey: "exec-1",
		Spec: workspacev1.CommandSpec{Argv: []string{"git", "status", "--short"}, WorkingDir: "repo", TimeoutSeconds: 30, OutputLimitBytes: 1 << 20},
	})
	if err != nil || view.State != workspacev1.CommandQueued || len(f.sessions.dispatched) != 0 {
		t.Fatalf("request handler executed instead of persisting intent: view=%#v dispatch=%v err=%v", view, f.sessions.dispatched, err)
	}
	view, err = f.service.Dispatch(context.Background(), f.scope, view.CommandID)
	if err != nil || view.State != workspacev1.CommandRunning || f.sessions.dispatched[view.CommandID] != 1 {
		t.Fatalf("outbox dispatcher did not delegate to workspace agent: view=%#v dispatch=%v err=%v", view, f.sessions.dispatched, err)
	}
	command, err := f.store.GetCommand(context.Background(), f.scope.TenantID, f.scope.ProjectID, view.CommandID)
	if err != nil || command.AgentSessionID != "mtls-session-1" || command.ExecutionVMID != "twc-vm-1" {
		t.Fatalf("execution evidence is not VM/mTLS-bound: command=%#v err=%v", command, err)
	}
}

func TestWorkspace_CommandPolicyRejectsForbiddenExecutableAndFlags(t *testing.T) {
	policy := DefaultCommandPolicy()
	base := workspacev1.CommandSpec{Argv: []string{"tofu", "plan"}, WorkingDir: "infrastructure", TimeoutSeconds: 30, OutputLimitBytes: 4096}
	if err := policy.Validate(base); err != nil {
		t.Fatal(err)
	}
	for _, argv := range [][]string{
		{"sudo", "tofu", "apply"}, {"docker", "run", "--privileged", "image"},
		{"buildctl", "build", "--network=host"}, {"git", "clone", "ssh://root@cloud"},
		{"../tofu", "apply"}, {"buildctl", "build", "--mount=/var/run/docker.sock"},
	} {
		spec := base
		spec.Argv = argv
		if !errors.Is(policy.Validate(spec), ErrPolicyDenied) {
			t.Fatalf("forbidden argv accepted: %v", argv)
		}
	}
}

func TestWorkspace_NetworkProfileAllowsRequiredAndDeniesSensitiveDestinations(t *testing.T) {
	f := newFixture()
	_ = f.ready(t)
	request := f.provider.requests[0]
	policy := request.NetworkIsolation
	if !policy.PrivateAddressOnly || !policy.DenyAllInbound || !policy.OutboundGatewayMTLS || policy.VPCID != "vpc-workspace" {
		t.Fatalf("workspace network is not isolated: %#v", policy)
	}
	for _, expected := range []string{"169.254.169.254/32", "192.168.73.0/24", "192.168.74.0/24"} {
		if !contains(policy.DeniedCIDRs, expected) {
			t.Fatalf("sensitive destination %s is not denied: %v", expected, policy.DeniedCIDRs)
		}
	}
	if !contains(policy.AllowedEgressHosts, "gitlab.com") || !containsInt(policy.DeniedDestinationPorts, 25) {
		t.Fatalf("required egress or SMTP deny missing: %#v", policy)
	}
	if !contains(policy.EgressGatewayCIDRs, "192.168.75.4/32") || !contains(policy.DNSResolverCIDRs, "192.168.75.1/32") {
		t.Fatalf("workspace traffic does not have an explicit proxy/DNS path: %#v", policy)
	}
}

func TestWorkspace_CommandTimeoutKillsProcessTreeAndMarksUsage(t *testing.T) {
	f := newFixture()
	ready := f.ready(t)
	view, err := f.service.Exec(context.Background(), ExecRequest{
		Scope: f.scope, WorkspaceID: ready.WorkspaceID, Kind: "build", IdempotencyKey: "timeout-1", CredentialLeases: []string{"command-lease"},
		Spec: workspacev1.CommandSpec{Argv: []string{"make", "build"}, WorkingDir: "repo", TimeoutSeconds: 2, OutputLimitBytes: 4096},
	})
	if err != nil {
		t.Fatal(err)
	}
	view, err = f.service.Dispatch(context.Background(), f.scope, view.CommandID)
	if err != nil {
		t.Fatal(err)
	}
	f.clock.Add(3 * time.Second)
	count, err := f.service.EnforceTimeouts(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	command, getErr := f.store.GetCommand(context.Background(), f.scope.TenantID, f.scope.ProjectID, view.CommandID)
	if getErr != nil || count != 1 || command.State != workspacev1.CommandRunning || command.CancelRequestedAt == nil || command.UsageFinishedAt != nil || f.leases.revoked["command-lease"] != 0 || len(f.sessions.canceled) != 1 {
		t.Fatalf("timeout cancellation intent incomplete: command=%#v count=%d canceled=%v leases=%v err=%v", command, count, f.sessions.canceled, f.leases.revoked, getErr)
	}
	if _, err := f.service.RecordOutcome(context.Background(), CommandOutcome{TenantID: f.scope.TenantID, ProjectID: f.scope.ProjectID, WorkspaceID: ready.WorkspaceID, CommandID: view.CommandID, AgentSessionID: "mtls-session-1", VMID: "twc-vm-1", State: workspacev1.CommandTimedOut, FinishedAt: f.clock.Now()}); err == nil {
		t.Fatal("timeout accepted without process-tree termination evidence")
	}
	if _, err := f.service.RecordOutcome(context.Background(), CommandOutcome{TenantID: f.scope.TenantID, ProjectID: f.scope.ProjectID, WorkspaceID: ready.WorkspaceID, CommandID: view.CommandID, AgentSessionID: "mtls-session-1", VMID: "twc-vm-1", State: workspacev1.CommandTimedOut, FinishedAt: f.clock.Now(), ProcessTreeTerminated: true}); err != nil {
		t.Fatal(err)
	}
	command, getErr = f.store.GetCommand(context.Background(), f.scope.TenantID, f.scope.ProjectID, view.CommandID)
	if getErr != nil || command.State != workspacev1.CommandTimedOut || command.UsageFinishedAt == nil || f.leases.revoked["command-lease"] != 1 {
		t.Fatalf("timeout evidence incomplete: command=%#v count=%d canceled=%v leases=%v err=%v", command, count, f.sessions.canceled, f.leases.revoked, getErr)
	}
}

func TestWorkspace_RestartRecoversDurableCommandOutcomeWithoutRepeatingApply(t *testing.T) {
	f := newFixture()
	f.provider.createLostResponse = true
	created, err := f.service.Create(context.Background(), CreateRequest{Scope: f.scope, Spec: f.spec, IdempotencyKey: "create-1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.Reconcile(context.Background(), created.WorkspaceID); err == nil {
		t.Fatal("lost provider response not surfaced")
	}
	if _, err := f.service.Reconcile(context.Background(), created.WorkspaceID); err != nil || len(f.provider.requests) != 1 {
		t.Fatalf("lost create response caused duplicate VM: creates=%d err=%v", len(f.provider.requests), err)
	}
	view, err := f.service.Exec(context.Background(), ExecRequest{
		Scope: f.scope, WorkspaceID: created.WorkspaceID, Kind: "infra_apply", SerializationKey: "staging", IdempotencyKey: "apply-1",
		Spec: workspacev1.CommandSpec{Argv: []string{"tofu", "apply", "saved.plan"}, WorkingDir: "infrastructure", TimeoutSeconds: 300, OutputLimitBytes: 1 << 20},
	})
	if err != nil {
		t.Fatal(err)
	}
	view, err = f.service.Dispatch(context.Background(), f.scope, view.CommandID)
	if err != nil {
		t.Fatal(err)
	}
	exit := 0
	finished := f.clock.Now().Add(10 * time.Second)
	f.clock.Add(10 * time.Second)
	outcome := CommandOutcome{TenantID: f.scope.TenantID, ProjectID: f.scope.ProjectID, WorkspaceID: created.WorkspaceID, CommandID: view.CommandID, AgentSessionID: "mtls-session-1", VMID: "twc-vm-1", State: workspacev1.CommandSucceeded, ExitCode: &exit, FinishedAt: finished}
	if _, err := f.service.RecordOutcome(context.Background(), outcome); err != nil {
		t.Fatal(err)
	}
	restarted := *f.service
	replayed, err := restarted.Exec(context.Background(), ExecRequest{
		Scope: f.scope, WorkspaceID: created.WorkspaceID, Kind: "infra_apply", SerializationKey: "staging", IdempotencyKey: "apply-1",
		Spec: workspacev1.CommandSpec{Argv: []string{"tofu", "apply", "saved.plan"}, WorkingDir: "infrastructure", TimeoutSeconds: 300, OutputLimitBytes: 1 << 20},
	})
	if err != nil || replayed.State != workspacev1.CommandSucceeded || f.sessions.dispatched[view.CommandID] != 1 {
		t.Fatalf("terminal apply was repeated: view=%#v dispatches=%d err=%v", replayed, f.sessions.dispatched[view.CommandID], err)
	}
}

func TestWorkspace_ConcurrentCommandPolicySerializesStatefulOperations(t *testing.T) {
	f := newFixture()
	ready := f.ready(t)
	exec := func(key string) (workspacev1.CommandView, error) {
		return f.service.Exec(context.Background(), ExecRequest{
			Scope: f.scope, WorkspaceID: ready.WorkspaceID, Kind: "infra_apply", SerializationKey: "staging", IdempotencyKey: key,
			Spec: workspacev1.CommandSpec{Argv: []string{"tofu", "apply", key + ".plan"}, WorkingDir: "infrastructure", TimeoutSeconds: 300, OutputLimitBytes: 1 << 20},
		})
	}
	first, err := exec("apply-1")
	if err != nil {
		t.Fatal(err)
	}
	second, err := exec("apply-2")
	if err != nil {
		t.Fatal(err)
	}
	first, err = f.service.Dispatch(context.Background(), f.scope, first.CommandID)
	if err != nil {
		t.Fatal(err)
	}
	second, err = f.service.Dispatch(context.Background(), f.scope, second.CommandID)
	if !errors.Is(err, ErrStatefulCommandBusy) || second.State != workspacev1.CommandQueued || len(f.sessions.dispatched) != 1 {
		t.Fatalf("concurrent stateful command was not serialized: second=%#v dispatch=%v err=%v", second, f.sessions.dispatched, err)
	}
	exit := 0
	f.clock.Add(time.Second)
	_, err = f.service.RecordOutcome(context.Background(), CommandOutcome{TenantID: f.scope.TenantID, ProjectID: f.scope.ProjectID, WorkspaceID: ready.WorkspaceID, CommandID: first.CommandID, AgentSessionID: "mtls-session-1", VMID: "twc-vm-1", State: workspacev1.CommandSucceeded, ExitCode: &exit, FinishedAt: f.clock.Now()})
	if err != nil {
		t.Fatal(err)
	}
	second, err = f.service.Dispatch(context.Background(), f.scope, second.CommandID)
	if err != nil || second.State != workspacev1.CommandRunning || len(f.sessions.dispatched) != 2 {
		t.Fatalf("queued command did not proceed after lock release: second=%#v dispatch=%v err=%v", second, f.sessions.dispatched, err)
	}
}

func contains(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func containsInt(values []int, expected int) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}
