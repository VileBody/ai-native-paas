package pivot_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	commerceapp "github.com/keir-research/ai-native-paas/internal/commerce/application"
	commercedomain "github.com/keir-research/ai-native-paas/internal/commerce/domain"
	commercememory "github.com/keir-research/ai-native-paas/internal/commerce/memory"
	infraapp "github.com/keir-research/ai-native-paas/internal/infrastructure/application"
	"github.com/keir-research/ai-native-paas/internal/workspace"
	"github.com/keir-research/ai-native-paas/internal/workspace/commercebudget"
	commercev1 "github.com/keir-research/ai-native-paas/pkg/contracts/commerce/v1"
	commercev2 "github.com/keir-research/ai-native-paas/pkg/contracts/commerce/v2"
	infrastructurev1 "github.com/keir-research/ai-native-paas/pkg/contracts/infrastructure/v1"
	workspacev1 "github.com/keir-research/ai-native-paas/pkg/contracts/workspace/v1"
)

type budgetClock struct{ now time.Time }

func (c budgetClock) Now() time.Time { return c.now }

type budgetIDs struct {
	mu   sync.Mutex
	next int
}

func (i *budgetIDs) NewID(prefix string) string {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.next++
	return fmt.Sprintf("%s-%d", prefix, i.next)
}

func (i *budgetIDs) New(prefix string) string { return i.NewID(prefix) }

type budgetSessions struct {
	dispatches int
	envelope   workspace.CommandEnvelope
}

func (*budgetSessions) Connected(context.Context, string, string) (bool, error) { return true, nil }
func (s *budgetSessions) Dispatch(_ context.Context, envelope workspace.CommandEnvelope) (workspace.DispatchReceipt, error) {
	s.dispatches++
	s.envelope = envelope
	return workspace.DispatchReceipt{CommandID: envelope.CommandID, WorkspaceID: envelope.WorkspaceID, VMID: "vm-budget", AgentSessionID: "session-budget", Accepted: true}, nil
}
func (*budgetSessions) RequestCancel(context.Context, string, string) error { return nil }
func (*budgetSessions) Close(context.Context, string) error                 { return nil }

func TestCost_TofuPlanProducesDeterministicNormalizedEstimate(t *testing.T) {
	service, _, _ := infrastructureFixture()
	first, err := service.Plan(context.Background(), planCommand("estimate-a", "staging", twoResourcePlan))
	if err != nil {
		t.Fatal(err)
	}
	reordered := `{"resource_changes":[
    {"type":"unknown_cache","provider_name":"example/unknown","address":"unknown_cache.app","change":{"actions":["create"]}},
    {"type":"twc_server","provider_name":"registry.opentofu.org/timeweb-cloud/timeweb-cloud","address":"twc_server.app","change":{"after":{"different":"ignored"},"actions":["create"]}}
  ],"terraform_version":"1.12.4","format_version":"1.2"}`
	second, err := service.Plan(context.Background(), planCommand("estimate-b", "staging", reordered))
	if err != nil {
		t.Fatal(err)
	}
	if first.Summary.PlanHash != second.Summary.PlanHash || first.Estimate.Version != second.Estimate.Version {
		t.Fatalf("normalized plan/estimate changed with input ordering: first=%#v second=%#v", first, second)
	}
	if first.Estimate.Minimum.MinorUnit != 125 || first.Estimate.Maximum.MinorUnit != 1_250_125 || !first.Estimate.ApprovalRequired {
		t.Fatalf("unexpected conservative 25%% estimate: %#v", first.Estimate)
	}
	if len(first.Estimate.Lines) != 2 || first.Estimate.Lines[0].Meter != "timeweb.server.month" || first.Estimate.Lines[1].PriceKnown {
		t.Fatalf("estimate lines are not canonical/provider-aware: %#v", first.Estimate.Lines)
	}
}

func TestCost_UnknownProviderPriceProducesRangeAndApprovalRequirement(t *testing.T) {
	service, _, now := infrastructureFixture()
	unknownPlan := `{"resource_changes":[{"address":"unknown_cache.app","provider_name":"example/unknown","type":"unknown_cache","change":{"actions":["create"]}}]}`
	result, err := service.Plan(context.Background(), planCommand("unknown-price", "staging", unknownPlan))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Estimate.Lines) != 1 || result.Estimate.Lines[0].PriceKnown || result.Estimate.Lines[0].ProviderCost.MinorUnit != 0 {
		t.Fatalf("unknown provider price was represented as exact: %#v", result.Estimate.Lines)
	}
	if result.Estimate.Minimum.MinorUnit != 0 || result.Estimate.Maximum.MinorUnit != 1_250_000 || !result.Estimate.ApprovalRequired || !result.Summary.RequiresApproval {
		t.Fatalf("unknown provider price did not produce conservative approval range: estimate=%#v summary=%#v", result.Estimate, result.Summary)
	}
	if result.Estimate.RateCardVersion != "2026-07-14" || result.Estimate.PriceSnapshotID != "timeweb-msk-2026-07-14" || result.Estimate.MarkupBasisPoints != 2500 {
		t.Fatalf("estimate lost immutable pricing inputs: %#v", result.Estimate)
	}
	authorization := infrastructurev1.ApplyAuthorization{
		PlanID: result.Summary.PlanID, PlanHash: result.Summary.PlanHash,
		EstimateVersion: result.Estimate.Version, ReservationID: result.Reservation.ReservationID,
		Target: "staging", ActorID: "agent-1", ExpiresAt: now.Add(10 * time.Minute),
	}
	if _, err := service.AuthorizeApply(context.Background(), infraapp.ApplyCommand{
		TenantID: "tenant-1", ProjectID: "project-1", IdempotencyKey: "unknown-price-apply", Authorization: authorization,
	}); !errors.Is(err, infraapp.ErrApprovalRequired) {
		t.Fatalf("unknown-price apply proceeded without exact-plan approval: %v", err)
	}
}

func TestCost_HelmResourcesContributeRequestedRuntimeAllocation(t *testing.T) {
	service, _, _ := infrastructureFixture()
	service.Prices.Prices[infraapp.RuntimeCPUPriceKey] = infraapp.UnitPrice{
		Meter: "runtime.cpu.request", Unit: "millicore-month", ProviderMinorPerQuantity: 2, Known: true,
	}
	service.Prices.Prices[infraapp.RuntimeMemoryPriceKey] = infraapp.UnitPrice{
		Meter: "runtime.memory.request", Unit: "mib-month", ProviderMinorPerQuantity: 1, Known: true,
	}
	service.Prices.Prices[infraapp.RuntimeStoragePriceKey] = infraapp.UnitPrice{
		Meter: "runtime.storage.request", Unit: "mib-month", ProviderMinorPerQuantity: 3, Known: true,
	}
	service.Prices.Prices[infraapp.RuntimeLoadBalancerPriceKey] = infraapp.UnitPrice{
		Meter: "runtime.load-balancer", Unit: "load-balancer-month", ProviderMinorPerQuantity: 100, Known: true,
	}
	command := planCommand("helm-runtime-allocation", "staging", knownResourcePlan)
	command.RenderedManifests = []byte(`apiVersion: apps/v1
kind: Deployment
metadata:
  name: api
  namespace: project-1-staging
spec:
  replicas: 3
  template:
    spec:
      containers:
        - name: api
          resources:
            requests:
              cpu: 250m
              memory: 128Mi
            limits:
              cpu: "8"
              memory: 16Gi
        - name: limits-only-sidecar
          resources:
            limits:
              cpu: "32"
              memory: 64Gi
---
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: data
  namespace: project-1-staging
spec:
  resources:
    requests:
      storage: 10Gi
---
apiVersion: v1
kind: Service
metadata:
  name: api
  namespace: project-1-staging
spec:
  type: LoadBalancer
`)
	result, err := service.Plan(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	lines := make(map[string]commercev2.EstimateLine, len(result.Estimate.Lines))
	for _, line := range result.Estimate.Lines {
		lines[line.Meter] = line
	}
	assertLine := func(meter string, quantity, providerCost int64) {
		t.Helper()
		line, exists := lines[meter]
		if !exists || line.Quantity != quantity || line.ProviderCost.MinorUnit != providerCost || !line.PriceKnown {
			t.Fatalf("unexpected %s estimate line: %#v", meter, line)
		}
	}
	assertLine("runtime.cpu.request", 750, 1500)
	assertLine("runtime.memory.request", 384, 384)
	assertLine("runtime.storage.request", 10*1024, 30*1024)
	assertLine("runtime.load-balancer", 1, 100)
	if result.Estimate.Minimum.MinorUnit != 41_005 || result.Estimate.Maximum.MinorUnit != 41_005 || result.Estimate.ApprovalRequired {
		t.Fatalf("unexpected requested-allocation estimate totals: %#v", result.Estimate)
	}

	withoutRuntime := planCommand("helm-runtime-allocation-control", "staging", knownResourcePlan)
	control, err := service.Plan(context.Background(), withoutRuntime)
	if err != nil {
		t.Fatal(err)
	}
	if result.Summary.PlanHash == control.Summary.PlanHash || result.Estimate.Version == control.Estimate.Version {
		t.Fatal("requested runtime allocation was not bound to plan and estimate identity")
	}

	for name, manifests := range map[string]string{
		"unknown daemonset cardinality": `apiVersion: apps/v1
kind: DaemonSet
metadata:
  name: node-agent
spec:
  template:
    spec:
      containers:
        - resources:
            requests:
              cpu: 100m
`,
		"invalid request quantity": `apiVersion: apps/v1
kind: Deployment
metadata:
  name: invalid
spec:
  template:
    spec:
      containers:
        - resources:
            requests:
              memory: definitely-not-a-quantity
`,
	} {
		t.Run(name, func(t *testing.T) {
			invalid := planCommand("helm-runtime-allocation-"+strings.ReplaceAll(name, " ", "-"), "staging", knownResourcePlan)
			invalid.RenderedManifests = []byte(manifests)
			if _, err := service.Plan(context.Background(), invalid); err == nil {
				t.Fatal("ambiguous or invalid requested runtime allocation was accepted")
			}
		})
	}
}

func TestCost_ApprovalInvalidatedWhenPlanHashChanges(t *testing.T) {
	service, _, now := infrastructureFixture()
	planA, err := service.Plan(context.Background(), planCommand("approval-plan-a", "production", knownResourcePlan))
	if err != nil {
		t.Fatal(err)
	}
	grantA, err := service.GrantApproval(context.Background(), infraapp.GrantApprovalCommand{
		TenantID: "tenant-1", ProjectID: "project-1", PlanID: planA.Summary.PlanID,
		ActorID: "agent-1", ApproverUserID: "human-1", ExpiresAt: now.Add(15 * time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	changed := `{"resource_changes":[{"address":"twc_server.changed","provider_name":"registry.opentofu.org/timeweb-cloud/timeweb-cloud","type":"twc_server","change":{"actions":["create"]}}]}`
	planB, err := service.Plan(context.Background(), planCommand("approval-plan-b", "production", changed))
	if err != nil {
		t.Fatal(err)
	}
	if planA.Summary.PlanHash == planB.Summary.PlanHash {
		t.Fatal("IaC change did not produce a new canonical plan hash")
	}
	authorization := infrastructurev1.ApplyAuthorization{
		PlanID: planB.Summary.PlanID, PlanHash: planB.Summary.PlanHash,
		EstimateVersion: planB.Estimate.Version, ReservationID: planB.Reservation.ReservationID,
		ApprovalGrantID: grantA.GrantID, Target: "production", ActorID: "agent-1",
		ExpiresAt: now.Add(10 * time.Minute),
	}
	if _, err = service.AuthorizeApply(context.Background(), infraapp.ApplyCommand{
		TenantID: "tenant-1", ProjectID: "project-1", IdempotencyKey: "apply-plan-b", Authorization: authorization,
	}); !errors.Is(err, infraapp.ErrPermissionDenied) {
		t.Fatalf("approval for plan A authorized plan B: %v", err)
	}
}

func TestUsage_PartialApplySettlesCreatedResourcesAndReleasesRemainder(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 14, 15, 0, 0, 0, time.UTC)
	ids := &budgetIDs{}
	store := commercememory.New()
	commerce := &commerceapp.Service{
		Store: store, Clock: budgetClock{now: now}, IDs: ids, Ownership: commerceapp.AllowAllOwnership{},
	}
	if _, err := commerce.CreatePlanDefinition(ctx, commerceapp.CreatePlanDefinitionCommand{ID: "partial-plan", Name: "Partial apply beta"}); err != nil {
		t.Fatal(err)
	}
	version, err := commerce.CreatePlanVersion(ctx, commerceapp.CreatePlanVersionCommand{
		ID: "partial-plan-v1", DefinitionID: "partial-plan", PolicyVersion: "partial-policy-v1", Number: 1,
		Spec: commercev1.PlanSpec{
			Currency: "RUB", Features: map[string]bool{"deploy": true},
			Quotas: map[string]int64{"project.budget.minor": 100},
			Prices: map[commercev1.Meter]commercev1.Price{commercev1.MeterDatabasePlanSeconds: {MinorUnits: 1, PerQuantity: 1}},
		},
		EffectiveFrom: now.Add(-time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = commerce.ActivatePlanVersion(ctx, version.ID); err != nil {
		t.Fatal(err)
	}
	_, period, err := commerce.StartSubscription(ctx, commerceapp.StartSubscriptionCommand{
		ID: "partial-subscription", TenantID: "tenant-partial", PlanVersionID: version.ID, PeriodID: "partial-period",
		State: commercedomain.SubscriptionActive, PeriodStart: now.Add(-time.Hour), PeriodEnd: now.Add(30 * 24 * time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	reservation, err := commerce.Reserve(ctx, commercev1.QuotaRequest{
		TenantID: "tenant-partial", ProjectID: "project-partial", Resource: "project.budget.minor", Quantity: 100,
		IdempotencyKey: "partial-apply-reservation", At: now, ExpiresAt: now.Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = commerce.Commit(ctx, reservation.ID); err != nil {
		t.Fatal(err)
	}
	command := commerceapp.SettleApplyReservationCommand{
		TenantID: "tenant-partial", ProjectID: "project-partial", OperationID: "operation-partial",
		ReservationID: reservation.ID, IdempotencyKey: "settle-partial-apply", ObservedAt: now,
		Resources: []commerceapp.ObservedResourceAllocation{{
			ExternalIdentity: "cozystack:postgresql/project-partial/main", ResourceType: "managed-database",
			Meter: commercev1.MeterDatabasePlanSeconds, UsageQuantity: 3600, ReservationQuantity: 40,
			WindowStart: now.Add(-time.Hour), WindowEnd: now, Metadata: map[string]string{"plan": "small"},
		}},
	}
	sibling, err := commerce.Reserve(ctx, commercev1.QuotaRequest{
		TenantID: "tenant-partial", ProjectID: "project-sibling", Resource: "project.budget.minor", Quantity: 100,
		IdempotencyKey: "sibling-project-reservation", At: now, ExpiresAt: now.Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("independent sibling project budget was not available: %v", err)
	}
	if err = commerce.Commit(ctx, sibling.ID); err != nil {
		t.Fatal(err)
	}
	wrongProject := command
	wrongProject.ReservationID = sibling.ID
	wrongProject.IdempotencyKey = "cross-project-settlement"
	if _, err = commerce.SettleApplyReservation(ctx, wrongProject); !commercedomain.HasCode(err, commercedomain.CodePermissionDenied) || len(store.Usage("tenant-partial", period.ID)) != 0 {
		t.Fatalf("sibling project reservation was settled or charged: usage=%#v err=%v", store.Usage("tenant-partial", period.ID), err)
	}
	result, err := commerce.SettleApplyReservation(ctx, command)
	if err != nil {
		t.Fatal(err)
	}
	if result.Settlement.State != commercev2.SettlementPartialRecoverable || !result.Settlement.Recoverable || result.Settlement.SettledQuantity != 40 || result.Settlement.ReleasedQuantity != 60 || result.Settlement.ObservedResourceCount != 1 || result.Reservation.State != "SETTLED" {
		t.Fatalf("unexpected partial settlement: %#v", result)
	}
	usage := store.Usage("tenant-partial", period.ID)
	if len(usage) != 1 || usage[0].Meter != commercev1.MeterDatabasePlanSeconds || usage[0].Quantity != 3600 || usage[0].Metadata["operation_id"] != "operation-partial" {
		t.Fatalf("created database usage was not settled once: %#v", usage)
	}
	replayed, err := commerce.SettleApplyReservation(ctx, command)
	if err != nil || replayed.Settlement.SettlementID != result.Settlement.SettlementID || len(store.Usage("tenant-partial", period.ID)) != 1 {
		t.Fatalf("settlement replay was not idempotent: result=%#v usage=%#v err=%v", replayed, store.Usage("tenant-partial", period.ID), err)
	}
	if _, err = commerce.Reserve(ctx, commercev1.QuotaRequest{
		TenantID: "tenant-partial", ProjectID: "project-partial", Resource: "project.budget.minor", Quantity: 60,
		IdempotencyKey: "released-remainder", At: now, ExpiresAt: now.Add(time.Hour),
	}); err != nil {
		t.Fatalf("released reservation remainder was unavailable: %v", err)
	}
	if _, err = commerce.Reserve(ctx, commercev1.QuotaRequest{
		TenantID: "tenant-partial", ProjectID: "project-partial", Resource: "project.budget.minor", Quantity: 1,
		IdempotencyKey: "oversubscribe-after-settlement", At: now, ExpiresAt: now.Add(time.Hour),
	}); !commercedomain.HasCode(err, commercedomain.CodeQuotaExceeded) {
		t.Fatalf("settled database allocation stopped counting against quota: %v", err)
	}
}

func TestQuota_ExpiredReservationCannotAuthorizeLateApply(t *testing.T) {
	service, store, now := infrastructureFixture()
	result, err := service.Plan(context.Background(), planCommand("expiring-reservation", "staging", knownResourcePlan))
	if err != nil {
		t.Fatal(err)
	}
	if result.Summary.RequiresApproval {
		t.Fatal("known-price staging plan unexpectedly requires approval")
	}
	service.Clock = fixedClock{now: now.Add(21 * time.Minute)}
	authorization := infrastructurev1.ApplyAuthorization{
		PlanID: result.Summary.PlanID, PlanHash: result.Summary.PlanHash,
		EstimateVersion: result.Estimate.Version, ReservationID: result.Reservation.ReservationID,
		Target: "staging", ActorID: "agent-1", ExpiresAt: now.Add(30 * time.Minute),
	}
	if _, err = service.AuthorizeApply(context.Background(), infraapp.ApplyCommand{
		TenantID: "tenant-1", ProjectID: "project-1", IdempotencyKey: "late-apply", Authorization: authorization,
	}); !errors.Is(err, infraapp.ErrPermissionDenied) {
		t.Fatalf("expired reservation authorized apply: %v", err)
	}
	stored, err := store.GetPlan(context.Background(), "tenant-1", "project-1", result.Summary.PlanID)
	if err != nil {
		t.Fatal(err)
	}
	if !stored.ApplyStartedAt.IsZero() {
		t.Fatalf("rejected late apply was marked started: %#v", stored)
	}
}

func TestBudget_WorkspaceCommandStoppedBeforeExceedingHardLimit(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 14, 13, 0, 0, 0, time.UTC)
	ids := &budgetIDs{}
	commerce := &commerceapp.Service{Store: commercememory.New(), Clock: budgetClock{now: now}, IDs: ids}
	if _, err := commerce.CreatePlanDefinition(ctx, commerceapp.CreatePlanDefinitionCommand{ID: "plan-workspace", Name: "Workspace beta"}); err != nil {
		t.Fatal(err)
	}
	if _, err := commerce.CreatePlanVersion(ctx, commerceapp.CreatePlanVersionCommand{
		ID: "plan-workspace-v1", DefinitionID: "plan-workspace", PolicyVersion: "workspace-budget-v1", Number: 1,
		Spec:          commercev1.PlanSpec{Currency: "RUB", Features: map[string]bool{}, Quotas: map[string]int64{commercebudget.Resource: 60}, Prices: map[commercev1.Meter]commercev1.Price{}},
		EffectiveFrom: now.Add(-time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := commerce.ActivatePlanVersion(ctx, "plan-workspace-v1"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := commerce.StartSubscription(ctx, commerceapp.StartSubscriptionCommand{
		ID: "subscription-workspace", TenantID: "tenant-budget", PlanVersionID: "plan-workspace-v1", PeriodID: "period-workspace",
		State: commercedomain.SubscriptionActive, PeriodStart: now.Add(-time.Hour), PeriodEnd: now.Add(30 * 24 * time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	consumed, err := commerce.Reserve(ctx, commercev1.QuotaRequest{
		TenantID: "tenant-budget", ProjectID: "project-budget", Resource: commercebudget.Resource, Quantity: 50, IdempotencyKey: "already-consumed",
		At: now, ExpiresAt: now.Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := commerce.Commit(ctx, consumed.ID); err != nil {
		t.Fatal(err)
	}

	store := workspace.NewMemoryStore()
	sessions := &budgetSessions{}
	workspaces := &workspace.Service{
		Store: store, Sessions: sessions, Budgets: commercebudget.Adapter{Quotas: commerce},
		Clock: budgetClock{now: now}, IDs: ids, Policy: workspace.DefaultCommandPolicy(),
	}
	scope := workspace.Scope{TenantID: "tenant-budget", ProjectID: "project-budget", ActorID: "agent-budget"}
	ref, err := workspaces.Create(ctx, workspace.CreateRequest{Scope: scope, IdempotencyKey: "workspace-budget", Spec: workspacev1.WorkspaceSpec{
		ProjectID: "project-budget", TaskID: "task-budget", ImageDigest: "sha256:" + strings.Repeat("a", 64),
		CPUMillis: 1000, MemoryMiB: 1024, TTLSeconds: 600, NetworkProfile: "isolated-governed",
	}})
	if err != nil {
		t.Fatal(err)
	}
	stored, err := store.GetWorkspace(ctx, scope.TenantID, scope.ProjectID, ref.WorkspaceID)
	if err != nil {
		t.Fatal(err)
	}
	expected := stored.Version
	stored.State = workspacev1.WorkspaceReady
	stored.ProviderVMID = "vm-budget"
	stored.Version++
	stored.UpdatedAt = now
	if err := store.UpdateWorkspace(ctx, stored, expected); err != nil {
		t.Fatal(err)
	}

	long, err := workspaces.Exec(ctx, workspace.ExecRequest{
		Scope: scope, WorkspaceID: ref.WorkspaceID, Kind: "build", IdempotencyKey: "long-command",
		Spec: workspacev1.CommandSpec{Argv: []string{"go", "test", "./..."}, TimeoutSeconds: 20, OutputLimitBytes: 4096},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := workspaces.Dispatch(ctx, scope, long.CommandID); err == nil || sessions.dispatches != 0 {
		t.Fatalf("over-budget command error=%v dispatches=%d", err, sessions.dispatches)
	}

	bounded, err := workspaces.Exec(ctx, workspace.ExecRequest{
		Scope: scope, WorkspaceID: ref.WorkspaceID, Kind: "build", IdempotencyKey: "bounded-command",
		Spec: workspacev1.CommandSpec{Argv: []string{"go", "test", "./..."}, TimeoutSeconds: 10, OutputLimitBytes: 4096},
	})
	if err != nil {
		t.Fatal(err)
	}
	view, err := workspaces.Dispatch(ctx, scope, bounded.CommandID)
	if err != nil || view.State != workspacev1.CommandRunning || sessions.dispatches != 1 {
		t.Fatalf("bounded command view=%+v error=%v dispatches=%d", view, err, sessions.dispatches)
	}
	if sessions.envelope.BudgetLease.GrantedSeconds != 10 || !sessions.envelope.BudgetLease.NotAfter.Equal(now.Add(10*time.Second)) {
		t.Fatalf("hard runtime lease=%+v", sessions.envelope.BudgetLease)
	}
}

func TestCost_ApprovalSummaryIncludesDestructionRiskAndMonthlyDelta(t *testing.T) {
	service, _, _ := infrastructureFixture()
	plan := `{"resource_changes":[
		{"address":"twc_server.new","provider_name":"timeweb","type":"twc_server","change":{"actions":["create"]}},
		{"address":"twc_server.changed","provider_name":"timeweb","type":"twc_server","change":{"actions":["update"]}},
		{"address":"twc_server.old","provider_name":"timeweb","type":"twc_server","change":{"actions":["delete"]}}
	]}`
	result, err := service.Plan(context.Background(), planCommand("approval-summary", "production", plan))
	if err != nil {
		t.Fatal(err)
	}
	summary, err := service.GetApprovalSummary(context.Background(), "tenant-1", "project-1", result.Summary.PlanID)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.MarshalIndent(summary, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	const golden = `{
  "plan_id": "plan-2",
  "project_id": "project-1",
  "target": "production",
  "plan_hash": "PLAN_HASH",
  "counts": {
    "create": 1,
    "update": 1,
    "replace": 0,
    "delete": 1,
    "no_op": 0
  },
  "destruction_risks": [
    {
      "address": "twc_server.old",
      "action": "DELETE",
      "reason": "resource deletion is irreversible"
    }
  ],
  "monthly_delta": {
    "currency": "RUB",
    "minimum_minor": -125,
    "maximum_minor": 125,
    "complete": false
  },
  "one_time_delta": {
    "currency": "RUB",
    "minimum_minor": 0,
    "maximum_minor": 0,
    "complete": false
  },
  "unknowns": [
    "monthly-delta:twc_server.changed",
    "one-time-price:twc_server.changed",
    "one-time-price:twc_server.new"
  ],
  "estimate_version": "ESTIMATE_VERSION",
  "reservation_id": "reservation-3",
  "approval_expires_at": "2026-07-14T09:50:00Z"
}`
	want := strings.ReplaceAll(golden, "PLAN_HASH", result.Summary.PlanHash)
	want = strings.ReplaceAll(want, "ESTIMATE_VERSION", result.Estimate.Version)
	if string(raw) != want {
		t.Fatalf("approval summary contract changed:\n%s\nwant:\n%s", raw, want)
	}
}

const knownResourcePlan = `{
  "resource_changes":[
    {"address":"twc_server.app","provider_name":"registry.opentofu.org/timeweb-cloud/timeweb-cloud","type":"twc_server","change":{"actions":["create"]}}
  ]
}`
