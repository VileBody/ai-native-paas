package execution

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	kernelv2 "github.com/keir-research/ai-native-paas/contracts/kernel/v2"
)

type testClock struct {
	mu  sync.RWMutex
	now time.Time
}

func (c *testClock) Now() time.Time {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.now
}

func (c *testClock) Set(value time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = value
}

type testIDs struct{ next atomic.Int64 }

func (i *testIDs) New(prefix string) string { return fmt.Sprintf("%s-%d", prefix, i.next.Add(1)) }

type fixture struct {
	service   *Service
	store     *MemoryStore
	clock     *testClock
	principal VerifiedPrincipal
	target    TargetScope
}

func newFixture(kind kernelv2.PrincipalKind) *fixture {
	now := time.Date(2026, time.July, 14, 12, 0, 0, 0, time.UTC)
	workspaceID := ""
	if kind == kernelv2.PrincipalWorkspace {
		workspaceID = "workspace-1"
	}
	store := NewMemoryStore()
	clock := &testClock{now: now}
	principal := VerifiedPrincipal{
		Identity: kernelv2.Principal{Kind: kind, SubjectID: "subject-1", TenantID: "tenant-1", ProjectID: "project-1", WorkspaceID: workspaceID, TaskID: "task-1"},
		Scopes:   map[string]struct{}{"workspace:exec": {}, "workflow:create": {}, "approval:grant": {}},
		Lease:    kernelv2.CredentialLease{LeaseID: "lease-1", Kind: "access-jwt", Audience: "project-mcp", ExpiresAt: now.Add(10 * time.Minute), RevocationID: "revoke-1"},
	}
	return &fixture{service: &Service{Store: store, Clock: clock, IDs: &testIDs{}}, store: store, clock: clock, principal: principal, target: TargetScope{TenantID: "tenant-1", ProjectID: "project-1", WorkspaceID: workspaceID}}
}

func oneNodeFactory(calls *atomic.Int64) GraphFactory {
	return GraphFactoryFunc(func(context.Context) ([]NodeSpec, error) {
		if calls != nil {
			calls.Add(1)
		}
		return []NodeSpec{{NodeID: "plan", Kind: "infra.plan", Cancelable: true}}, nil
	})
}

func createCommand(f *fixture, key string, payload any, factory GraphFactory) CreateWorkflowCommand {
	return CreateWorkflowCommand{Principal: f.principal, Target: f.target, RequiredScope: "workflow:create", IdempotencyKey: key, Arguments: json.RawMessage(`{"environment":"preview"}`), Payload: payload, Factory: factory}
}

func TestKernel_ProjectScopedPrincipalCannotCrossProject(t *testing.T) {
	f := newFixture(kernelv2.PrincipalService)
	command := createCommand(f, "key-1", map[string]any{"action": "plan"}, oneNodeFactory(nil))
	command.Target.ProjectID = "project-2"
	var calls atomic.Int64
	command.Factory = oneNodeFactory(&calls)
	if _, err := f.service.CreateWorkflow(context.Background(), command); !IsCode(err, CodePermissionDenied) {
		t.Fatalf("want permission denied, got %v", err)
	}
	if calls.Load() != 0 || f.store.GraphCount() != 0 {
		t.Fatalf("denied request reached domain/external work: calls=%d graphs=%d", calls.Load(), f.store.GraphCount())
	}
	audit := f.store.Audit()
	if len(audit) != 1 || audit[0].Outcome != "DENIED" || audit[0].ProjectID != "project-1" {
		t.Fatalf("missing scoped denial audit: %#v", audit)
	}
}

func TestKernel_WorkspacePrincipalCannotEscalateToTenantScope(t *testing.T) {
	f := newFixture(kernelv2.PrincipalWorkspace)
	command := createCommand(f, "key-1", map[string]any{"action": "exec"}, oneNodeFactory(nil))
	command.Arguments = json.RawMessage(`{"nested":{"tenant_id":"tenant-1","project_id":"project-2"}}`)
	var calls atomic.Int64
	command.Factory = oneNodeFactory(&calls)
	if _, err := f.service.CreateWorkflow(context.Background(), command); !IsCode(err, CodePermissionDenied) {
		t.Fatalf("want scope override denial, got %v", err)
	}
	if calls.Load() != 0 || f.store.GraphCount() != 0 {
		t.Fatal("workspace escalation reached graph factory or store")
	}
	command.Arguments = json.RawMessage(`{"environment":"preview"}`)
	command.Target.WorkspaceID = "workspace-2"
	if _, err := f.service.CreateWorkflow(context.Background(), command); !IsCode(err, CodePermissionDenied) {
		t.Fatalf("want workspace boundary denial, got %v", err)
	}
}

func TestKernel_CredentialLeaseExpiresAndCannotBeReused(t *testing.T) {
	f := newFixture(kernelv2.PrincipalAgent)
	started := f.clock.Now()
	f.clock.Set(started.Add(9 * time.Minute))
	command := createCommand(f, "key-1", map[string]any{"action": "plan"}, oneNodeFactory(nil))
	first, err := f.service.CreateWorkflow(context.Background(), command)
	if err != nil {
		t.Fatalf("valid lease rejected: %v", err)
	}
	f.clock.Set(started.Add(11 * time.Minute))
	second, err := f.service.CreateWorkflow(context.Background(), command)
	if !IsCode(err, CodeCredentialExpired) || second != nil {
		t.Fatalf("expired replay accepted: graph=%#v err=%v", second, err)
	}
	if f.store.GraphCount() != 1 || first.GraphID == "" {
		t.Fatal("expired replay mutated stored workflow")
	}
}

func TestKernel_OperationWaitsForApprovalWithoutRepeatingSideEffect(t *testing.T) {
	f := newFixture(kernelv2.PrincipalAgent)
	graph, err := f.service.CreateWorkflow(context.Background(), createCommand(f, "key-1", map[string]any{"action": "plan"}, oneNodeFactory(nil)))
	if err != nil {
		t.Fatal(err)
	}
	var providerCalls atomic.Int64
	effect := func(context.Context) (json.RawMessage, *kernelv2.CredentialLease, error) {
		providerCalls.Add(1)
		return json.RawMessage(`{"changes":[{"address":"vm.main","action":"CREATE"}]}`), nil, nil
	}
	waiting, err := f.service.EnsureCheckpoint(context.Background(), graph.GraphID, "plan", "opentofu.plan", effect)
	if err != nil {
		t.Fatal(err)
	}
	restarted, err := f.service.EnsureCheckpoint(context.Background(), graph.GraphID, "plan", "opentofu.plan", effect)
	if err != nil {
		t.Fatal(err)
	}
	if providerCalls.Load() != 1 || waiting.ParentState != kernelv2.OperationWaitingApproval || restarted.ParentState != kernelv2.OperationWaitingApproval {
		t.Fatalf("plan side effect repeated or state lost: calls=%d waiting=%s restarted=%s", providerCalls.Load(), waiting.ParentState, restarted.ParentState)
	}
	checkpoint := restarted.Nodes[0].Checkpoint
	binding := kernelv2.ApprovalBinding{ApprovalGrantID: "grant-1", PlanHash: checkpoint.PayloadHash, EstimateVersion: "estimate-1", Target: "preview", ActorID: "user-1", ExpiresAt: f.clock.Now().Add(time.Minute)}
	resumed, err := f.service.ResumeApproved(context.Background(), graph.GraphID, "plan", binding)
	if err != nil || resumed.Nodes[0].State != kernelv2.OperationRunning {
		t.Fatalf("resume from checkpoint failed: graph=%#v err=%v", resumed, err)
	}
}

func TestKernel_ParentCancellationPropagatesToCancelableChildren(t *testing.T) {
	f := newFixture(kernelv2.PrincipalAgent)
	factory := GraphFactoryFunc(func(context.Context) ([]NodeSpec, error) {
		return []NodeSpec{{NodeID: "build", Kind: "build", Cancelable: true}, {NodeID: "plan", Kind: "plan", DependsOn: []string{"build"}, Cancelable: true}, {NodeID: "gitops", Kind: "gitops", DependsOn: []string{"plan"}, Cancelable: true}}, nil
	})
	graph, err := f.service.CreateWorkflow(context.Background(), createCommand(f, "key-1", map[string]any{"action": "deploy"}, factory))
	if err != nil {
		t.Fatal(err)
	}
	graph, err = f.store.UpdateGraph(context.Background(), graph.GraphID, graph.Version, func(candidate *Graph) error { return candidate.CompleteNode("build", f.clock.Now()) })
	if err != nil {
		t.Fatal(err)
	}
	canceling, children, err := f.service.Cancel(context.Background(), graph.GraphID)
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(children) != "[gitops plan]" || canceling.ParentState != kernelv2.OperationWaitingDependency || canceling.Nodes[0].State != kernelv2.OperationSucceeded {
		t.Fatalf("unexpected cancellation propagation: children=%v graph=%#v", children, canceling)
	}
	for _, child := range children {
		canceling, err = f.service.AcknowledgeCancellation(context.Background(), graph.GraphID, child)
		if err != nil {
			t.Fatal(err)
		}
	}
	if canceling.ParentState != kernelv2.OperationCanceled || canceling.Nodes[0].State != kernelv2.OperationSucceeded {
		t.Fatalf("parent did not wait for cancellation acknowledgements: %#v", canceling)
	}
}

func TestKernel_IdempotentCommandReturnsOriginalOperationGraph(t *testing.T) {
	f := newFixture(kernelv2.PrincipalAgent)
	command := createCommand(f, "same-key", map[string]any{"project": "hello", "action": "create"}, oneNodeFactory(nil))
	const concurrency = 32
	results := make(chan string, concurrency)
	errorsFound := make(chan error, concurrency)
	var wait sync.WaitGroup
	for range concurrency {
		wait.Add(1)
		go func() {
			defer wait.Done()
			graph, err := f.service.CreateWorkflow(context.Background(), command)
			if err != nil {
				errorsFound <- err
				return
			}
			results <- graph.GraphID
		}()
	}
	wait.Wait()
	close(results)
	close(errorsFound)
	for err := range errorsFound {
		t.Fatalf("concurrent create failed: %v", err)
	}
	identity := ""
	for graphID := range results {
		if identity == "" {
			identity = graphID
		} else if identity != graphID {
			t.Fatalf("idempotency returned multiple graph IDs: %s and %s", identity, graphID)
		}
	}
	if identity == "" || f.store.GraphCount() != 1 {
		t.Fatalf("want one durable graph, id=%q count=%d", identity, f.store.GraphCount())
	}
}

func TestKernel_IdempotencyPayloadMismatchCannotReuseApproval(t *testing.T) {
	f := newFixture(kernelv2.PrincipalAgent)
	first, err := f.service.CreateWorkflow(context.Background(), createCommand(f, "same-key", map[string]any{"plan_hash": "A"}, oneNodeFactory(nil)))
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.service.EnsureCheckpoint(context.Background(), first.GraphID, "plan", "opentofu.plan", func(context.Context) (json.RawMessage, *kernelv2.CredentialLease, error) {
		return json.RawMessage(`{"plan_hash":"A"}`), nil, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := f.service.CreateWorkflow(context.Background(), createCommand(f, "same-key", map[string]any{"plan_hash": "B"}, oneNodeFactory(nil)))
	if !IsCode(err, CodeIdempotencyConflict) || second != nil {
		t.Fatalf("payload mismatch reused stored approval/result: graph=%#v err=%v", second, err)
	}
}

func TestKernel_ServicePrincipalCannotApproveHumanAction(t *testing.T) {
	servicePrincipal := newFixture(kernelv2.PrincipalService)
	if err := servicePrincipal.service.GrantApproval(servicePrincipal.principal, "approval:grant"); !IsCode(err, CodePermissionDenied) {
		t.Fatalf("service principal approved human action: %v", err)
	}
	human := newFixture(kernelv2.PrincipalUser)
	if err := human.service.GrantApproval(human.principal, "approval:grant"); err != nil {
		t.Fatalf("verified human approval rejected: %v", err)
	}
}

func TestAgent_ToolArgumentsCannotOverrideVerifiedScope(t *testing.T) {
	for _, raw := range []string{
		`{"tenant_id":"tenant-2"}`,
		`{"nested":{"project_id":"project-2"}}`,
		`{"items":[{"workspace_id":"workspace-2"}]}`,
		`{"deep":{"deeper":{"agent_id":"agent-2"}}}`,
	} {
		if err := RejectScopeOverrides(json.RawMessage(raw)); !IsCode(err, CodePermissionDenied) {
			t.Fatalf("scope override accepted for %s: %v", raw, err)
		}
	}
	if err := RejectScopeOverrides(json.RawMessage(`{"environment":"preview","parameters":{"region":"ru-1"}}`)); err != nil {
		t.Fatalf("ordinary tool arguments rejected: %v", err)
	}
}
