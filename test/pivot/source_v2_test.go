package pivot_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/keir-research/ai-native-paas/internal/agent/enrollment"
	projectapp "github.com/keir-research/ai-native-paas/internal/project/application"
	sourceapp "github.com/keir-research/ai-native-paas/internal/source/application"
	"github.com/keir-research/ai-native-paas/internal/source/domain"
	"github.com/keir-research/ai-native-paas/internal/source/gitlab"
	"github.com/keir-research/ai-native-paas/internal/source/memory"
	"github.com/keir-research/ai-native-paas/internal/source/testkit"
	sourcehook "github.com/keir-research/ai-native-paas/internal/source/webhook"
	commercev2 "github.com/keir-research/ai-native-paas/pkg/contracts/commerce/v2"
	infrastructurev1 "github.com/keir-research/ai-native-paas/pkg/contracts/infrastructure/v1"
	sourcev2 "github.com/keir-research/ai-native-paas/pkg/contracts/source/v2"
)

type sourceV2Fixture struct {
	service    *sourceapp.Service
	hooks      *sourceapp.WebhookService
	store      *memory.Store
	provider   *testkit.Provider
	clock      *testkit.Clock
	ids        *testkit.IDs
	project    domain.Project
	repository domain.Repository
}

type sourcePurgeAuthorizer struct {
	allowed bool
	calls   int
	seen    sourceapp.ProjectPurgeAuthorization
}

type sourceEnrollmentIssuer struct {
	binding enrollment.Binding
}

func (i *sourceEnrollmentIssuer) Issue(_ context.Context, binding enrollment.Binding) (enrollment.EnrollmentToken, error) {
	i.binding = binding
	return enrollment.EnrollmentToken{EnrollmentID: "enrollment-source-v2", Token: "one-time-source-v2", ExpiresAt: time.Now().Add(enrollment.EnrollmentTTL)}, nil
}

func (a *sourcePurgeAuthorizer) VerifyAndConsumeProjectPurge(_ context.Context, authorization sourceapp.ProjectPurgeAuthorization) error {
	a.calls++
	a.seen = authorization
	if !a.allowed {
		return fmt.Errorf("approval denied")
	}
	return nil
}

func newSourceV2Fixture(t *testing.T) sourceV2Fixture {
	t.Helper()
	store := memory.New()
	provider := testkit.NewProvider()
	clock := &testkit.Clock{T: time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)}
	ids := &testkit.IDs{}
	service := &sourceapp.Service{Store: store, Provider: provider, Clock: clock, IDs: ids}
	created, err := service.CreateProject(context.Background(), sourceapp.CreateProjectCommand{
		TenantID: "tenant-1", ActorID: "user-1", Name: "Source recovery", ProviderNamespaceID: 10, IdempotencyKey: "project-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	repository, err := service.ProvisionRepository(context.Background(), sourceapp.ProvisionRepositoryCommand{TenantID: "tenant-1", ActorID: "user-1", RepositoryID: created.Repository.ID})
	if err != nil {
		t.Fatal(err)
	}
	hooks := &sourceapp.WebhookService{
		Store: store, Provider: provider, Verifier: sourcehook.Verifier{Secret: []byte("source-v2-webhook-secret")},
		Normalizer: sourcehook.Normalizer{Provider: "gitlab"}, Clock: clock, IDs: ids,
	}
	return sourceV2Fixture{service: service, hooks: hooks, store: store, provider: provider, clock: clock, ids: ids, project: created.Project, repository: repository}
}

func sourceV2PushBody(projectID int64, before, after, branch string, occurredAt time.Time) []byte {
	return []byte(fmt.Sprintf(`{"object_kind":"push","before":"%s","after":"%s","ref":"refs/heads/%s","project":{"id":%d},"event_created_at":"%s"}`, before, after, branch, projectID, occurredAt.Format(time.RFC3339Nano)))
}

func sourceV2Headers(body []byte, eventID string, signedAt time.Time) map[string][]string {
	timestamp, signature := sourcehook.Sign([]byte("source-v2-webhook-secret"), eventID, signedAt, body)
	return map[string][]string{"webhook-id": {eventID}, "webhook-timestamp": {timestamp}, "webhook-signature": {signature}}
}

func sourceV2Outbox(store *memory.Store, topic string) []sourceapp.OutboxRecord {
	_, _, outbox, _ := store.Snapshot()
	var result []sourceapp.OutboxRecord
	for _, record := range outbox {
		if record.Topic == topic {
			result = append(result, record)
		}
	}
	return result
}

func TestSource_CreateProjectBootstrapsV2RepositoryLayout(t *testing.T) {
	baseRevision := strings.Repeat("a", 40)
	bootstrapRevision := strings.Repeat("b", 40)
	currentHead := baseRevision
	bootstrapFiles := map[string][]byte{}
	projectCreates := 0
	bootstrapCommits := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v4/projects":
			projectCreates++
			var body struct {
				NamespaceID int64  `json:"namespace_id"`
				Path        string `json:"path"`
				Visibility  string `json:"visibility"`
				Description string `json:"description"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body.NamespaceID != 77 || body.Path != "booking" || body.Visibility != "private" || !strings.Contains(body.Description, "paas-correlation:") {
				t.Fatalf("unsafe project create body: %+v", body)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": 42, "namespace": map[string]any{"id": 77}, "path": "booking", "path_with_namespace": "beta/booking",
				"web_url": "https://gitlab.example/beta/booking", "default_branch": "main", "description": body.Description,
				"topics": []string{"ai-native-paas"},
			})
		case r.Method == http.MethodPost && r.URL.Path == "/api/v4/projects/42/protected_branches":
			w.WriteHeader(http.StatusCreated)
		case r.Method == http.MethodGet && r.URL.Path == "/api/v4/projects/42/repository/branches/main":
			_, _ = fmt.Fprintf(w, `{"commit":{"id":%q}}`, currentHead)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v4/projects/42/repository/commits":
			bootstrapCommits++
			var body struct {
				Branch        string `json:"branch"`
				StartSHA      string `json:"start_sha"`
				CommitMessage string `json:"commit_message"`
				Actions       []struct {
					Path     string `json:"file_path"`
					Content  string `json:"content"`
					Encoding string `json:"encoding"`
				} `json:"actions"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body.Branch != "main" || body.StartSHA != baseRevision || !strings.Contains(body.CommitMessage, "PaaS-Correlation:") {
				t.Fatalf("bootstrap is not exact-SHA bound: %+v", body)
			}
			for _, action := range body.Actions {
				if action.Encoding != "base64" {
					t.Fatalf("unexpected bootstrap encoding for %s", action.Path)
				}
				content, err := base64.StdEncoding.DecodeString(action.Content)
				if err != nil {
					t.Fatal(err)
				}
				bootstrapFiles[action.Path] = content
			}
			currentHead = bootstrapRevision
			_, _ = fmt.Fprintf(w, `{"id":%q}`, bootstrapRevision)
		default:
			t.Fatalf("unexpected GitLab request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	store := memory.New()
	clock := &testkit.Clock{T: time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)}
	ids := &testkit.IDs{}
	provider := &gitlab.Client{BaseURL: server.URL}
	source := &sourceapp.Service{Store: store, Provider: provider, Clock: clock, IDs: ids}
	enrollmentIssuer := &sourceEnrollmentIssuer{}
	projects := &projectapp.Service{
		Source: source, Bootstrapper: provider, Enrollment: enrollmentIssuer, GitLabNamespaceID: 77,
		MCPBaseURL: "https://mcp.example.test", WorkspaceImageDigest: "sha256:" + strings.Repeat("c", 64),
	}
	created, err := projects.Create(context.Background(), projectapp.CreateCommand{TenantID: "tenant-1", UserID: "user-1", Name: "booking", IdempotencyKey: "create-source-v2"})
	if err != nil {
		t.Fatal(err)
	}
	repository, err := source.GetRepositoryForProject(context.Background(), "tenant-1", created.ProjectID)
	if err != nil || repository.ProviderProjectID != 42 || repository.BootstrapRevision != bootstrapRevision {
		t.Fatalf("repository=%+v err=%v", repository, err)
	}
	var branch domain.BranchHead
	if err = store.Transact(context.Background(), func(tx sourceapp.Tx) error {
		branch, _ = tx.GetBranch(repository.ID, repository.DefaultBranch)
		return nil
	}); err != nil || branch.CommitSHA != bootstrapRevision {
		t.Fatalf("bootstrap branch=%+v err=%v", branch, err)
	}
	for _, path := range []string{"README.md", ".gitignore", "platform.yaml", "infrastructure/tofu/main.tf", "deploy/base/kustomization.yaml", "deploy/environments/development/kustomization.yaml", "deploy/environments/production/kustomization.yaml", "recipes.lock.yaml"} {
		if len(bootstrapFiles[path]) == 0 {
			t.Fatalf("bootstrap file missing: %s", path)
		}
	}
	if projectCreates != 1 || bootstrapCommits != 1 || enrollmentIssuer.binding.ProjectID != created.ProjectID {
		t.Fatalf("creates=%d commits=%d binding=%+v", projectCreates, bootstrapCommits, enrollmentIssuer.binding)
	}
	if len(sourceV2Outbox(store, "source.repository_bootstrapped.v2")) != 1 || len(sourceV2Outbox(store, sourcev2.EventRevisionObserved)) != 1 {
		t.Fatal("bootstrap revision evidence was not emitted exactly once")
	}
	replayed, err := source.RecordBootstrapRevision(context.Background(), sourceapp.RecordBootstrapRevisionCommand{
		TenantID: "tenant-1", ActorID: "user-1", RepositoryID: repository.ID, Revision: strings.ToUpper(bootstrapRevision),
	})
	if err != nil || replayed.BootstrapRevision != bootstrapRevision || len(sourceV2Outbox(store, "source.repository_bootstrapped.v2")) != 1 {
		t.Fatalf("bootstrap revision replay=%+v err=%v", replayed, err)
	}
}

func TestSource_MissedWebhookRecoveredByBranchReconciler(t *testing.T) {
	fixture := newSourceV2Fixture(t)
	observedA := strings.Repeat("a", 40)
	providerB := strings.Repeat("b", 40)
	if err := fixture.store.Transact(context.Background(), func(tx sourceapp.Tx) error {
		branch, err := domain.NewBranchHead(fixture.repository.ID, fixture.repository.DefaultBranch)
		if err != nil {
			return err
		}
		if _, err = branch.SetAuthoritative(observedA, fixture.clock.Now()); err != nil {
			return err
		}
		return tx.UpsertBranch(branch, 0)
	}); err != nil {
		t.Fatal(err)
	}
	fixture.provider.SetHead(fixture.repository.ProviderProjectID, fixture.repository.DefaultBranch, providerB)
	reconciler := sourceapp.Reconciler{Store: fixture.store, Provider: fixture.provider, Clock: fixture.clock, IDs: fixture.ids}
	first, err := reconciler.ReconcileAll(context.Background())
	if err != nil || first.BranchChanges != 1 {
		t.Fatalf("first reconcile=%+v err=%v", first, err)
	}
	second, err := reconciler.ReconcileAll(context.Background())
	if err != nil || second.BranchChanges != 0 {
		t.Fatalf("second reconcile=%+v err=%v", second, err)
	}
	events := sourceV2Outbox(fixture.store, sourcev2.EventRevisionObserved)
	if len(events) != 1 {
		t.Fatalf("revision events=%d", len(events))
	}
	var event sourcev2.RevisionObservedEvent
	if err := json.Unmarshal(events[0].Payload, &event); err != nil || event.Validate() != nil || event.CommitSHA != providerB || event.Reason != "reconciliation" {
		t.Fatalf("event=%+v err=%v", event, err)
	}
}

func TestSource_OutOfOrderWebhookCannotRegressObservedHead(t *testing.T) {
	fixture := newSourceV2Fixture(t)
	shaA := strings.Repeat("a", 40)
	shaB := strings.Repeat("b", 40)
	fixture.provider.SetHead(fixture.repository.ProviderProjectID, "main", shaB)
	newer := sourceV2PushBody(fixture.repository.ProviderProjectID, shaA, shaB, "main", fixture.clock.Now())
	if result, err := fixture.hooks.Handle(context.Background(), "tenant-1", sourceV2Headers(newer, "event-b", fixture.clock.Now()), newer); err != nil || result.Stale {
		t.Fatalf("newer result=%+v err=%v", result, err)
	}
	olderAt := fixture.clock.Now().Add(-time.Minute)
	older := sourceV2PushBody(fixture.repository.ProviderProjectID, strings.Repeat("0", 40), shaA, "main", olderAt)
	result, err := fixture.hooks.Handle(context.Background(), "tenant-1", sourceV2Headers(older, "event-a", fixture.clock.Now()), older)
	if err != nil || !result.Stale || result.Duplicate {
		t.Fatalf("older result=%+v err=%v", result, err)
	}
	replay, err := fixture.hooks.Handle(context.Background(), "tenant-1", sourceV2Headers(older, "event-a", fixture.clock.Now()), older)
	if err != nil || !replay.Duplicate {
		t.Fatalf("stale receipt was not durable: result=%+v err=%v", replay, err)
	}
	var branch domain.BranchHead
	if err := fixture.store.Transact(context.Background(), func(tx sourceapp.Tx) error {
		var ok bool
		branch, ok = tx.GetBranch(fixture.repository.ID, "main")
		if !ok {
			return fmt.Errorf("branch missing")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if branch.CommitSHA != shaB || branch.LastEventID != "event-b" {
		t.Fatalf("branch regressed: %+v", branch)
	}
	if events := sourceV2Outbox(fixture.store, sourcev2.EventRevisionObserved); len(events) != 1 {
		t.Fatalf("revision events=%d", len(events))
	}
	_, _, _, audit := fixture.store.Snapshot()
	if audit[len(audit)-1].Action != "source.push.stale" {
		t.Fatalf("old delivery not classified as stale: %+v", audit[len(audit)-1])
	}
}

func TestSource_DeletedBranchProducesEnvironmentCleanupIntent(t *testing.T) {
	fixture := newSourceV2Fixture(t)
	branchName := "preview/mr-7"
	environmentID := "env-preview-7"
	if _, err := fixture.service.BindPreviewEnvironment(context.Background(), sourceapp.BindPreviewEnvironmentCommand{
		TenantID: "tenant-1", ActorID: "gitops-controller", RepositoryID: fixture.repository.ID, Branch: branchName, EnvironmentID: environmentID,
	}); err != nil {
		t.Fatal(err)
	}
	before := strings.Repeat("c", 40)
	deleted := strings.Repeat("0", 40)
	body := sourceV2PushBody(fixture.repository.ProviderProjectID, before, deleted, branchName, fixture.clock.Now())
	result, err := fixture.hooks.Handle(context.Background(), "tenant-1", sourceV2Headers(body, "delete-preview-7", fixture.clock.Now()), body)
	if err != nil || result.Stale || result.Duplicate {
		t.Fatalf("delete result=%+v err=%v", result, err)
	}
	if _, err = fixture.hooks.Handle(context.Background(), "tenant-1", sourceV2Headers(body, "delete-preview-7", fixture.clock.Now()), body); err != nil {
		t.Fatal(err)
	}
	events := sourceV2Outbox(fixture.store, sourcev2.EventEnvironmentCleanupRequested)
	if len(events) != 1 {
		t.Fatalf("cleanup intents=%d", len(events))
	}
	var event sourcev2.EnvironmentCleanupRequestedEvent
	if err := json.Unmarshal(events[0].Payload, &event); err != nil || event.Validate() != nil || event.EnvironmentID != environmentID || event.Branch != branchName {
		t.Fatalf("event=%+v err=%v", event, err)
	}
	var branch domain.BranchHead
	if err := fixture.store.Transact(context.Background(), func(tx sourceapp.Tx) error {
		var ok bool
		branch, ok = tx.GetBranch(fixture.repository.ID, branchName)
		if !ok {
			return fmt.Errorf("branch missing")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if branch.EnvironmentID != environmentID || branch.DeletedAt.IsZero() {
		t.Fatalf("deletion state not durable: %+v", branch)
	}
	_, _, outbox, _ := fixture.store.Snapshot()
	for _, record := range outbox {
		if strings.Contains(record.Topic, "runtime.delete") || strings.Contains(record.Topic, "purge") {
			t.Fatalf("source attempted direct runtime deletion: %s", record.Topic)
		}
	}
}

func TestSource_MergeRequestPublishesPlanSummaryWithoutSecrets(t *testing.T) {
	fixture := newSourceV2Fixture(t)
	branch := "agent/plan-summary"
	head := strings.Repeat("b", 40)
	planHash := "sha256:" + strings.Repeat("c", 64)
	fixture.provider.SetHead(fixture.repository.ProviderProjectID, branch, head)
	mergeRequest, err := fixture.service.CreateMergeRequest(context.Background(), sourceapp.CreateMergeRequestCommand{
		TenantID: "tenant-1", ActorID: "agent-1", RepositoryID: fixture.repository.ID,
		SourceBranch: branch, TargetBranch: "main", ExpectedHeadSHA: head, SourcePlanHash: planHash,
		TaskID: "task-1", CorrelationID: "correlation-1", Title: "Plan summary", IdempotencyKey: "mr-summary-create",
	})
	if err != nil {
		t.Fatal(err)
	}
	const secretSentinel = "provider-secret-sentinel"
	plan := infrastructurev1.PlanSummary{
		PlanRef: infrastructurev1.PlanRef{
			PlanID: "plan-1", ProjectID: fixture.repository.ProjectID, WorkspaceID: "workspace-1", SourceSHA: head,
			PlanHash: planHash, StateGeneration: 7, CreatedAt: fixture.clock.Now(),
		},
		Changes: []infrastructurev1.ResourceChange{
			{Address: "twc_server." + secretSentinel, Provider: secretSentinel, ResourceType: "twc_server", Action: infrastructurev1.ActionCreate, ExternalID: secretSentinel},
			{Address: "twc_server.api", Provider: "timeweb", ResourceType: "twc_server", Action: infrastructurev1.ActionUpdate},
			{Address: "twc_database.old", Provider: "timeweb", ResourceType: "twc_database", Action: infrastructurev1.ActionDelete},
		},
		Destructive: true, RequiresApproval: true, EstimateVersion: "estimate-version-1", EstimateFingerprint: "sha256:" + strings.Repeat("d", 64),
	}
	estimate := commercev2.CostEstimate{
		EstimateID: "estimate-1", Version: plan.EstimateVersion, PlanHash: planHash, RateCardID: "beta-25",
		Lines:   []commercev2.EstimateLine{{Meter: secretSentinel, Quantity: 1, Unit: "resource-month", ProviderCost: commercev2.Money{Currency: "RUB", MinorUnit: 100}, CustomerCost: commercev2.Money{Currency: "RUB", MinorUnit: 125}, PriceKnown: true}},
		Minimum: commercev2.Money{Currency: "RUB", MinorUnit: 125}, Maximum: commercev2.Money{Currency: "RUB", MinorUnit: 750},
		ApprovalRequired: true, ExpiresAt: fixture.clock.Now().Add(30 * time.Minute),
	}
	var noteBody string
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path != fmt.Sprintf("/api/v4/projects/%d/merge_requests/%d/notes", fixture.repository.ProviderProjectID, mergeRequest.ProviderIID) {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		switch r.Method {
		case http.MethodGet:
			_, _ = io.WriteString(w, `[]`)
		case http.MethodPost:
			var request struct {
				Body string `json:"body"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Fatal(err)
			}
			noteBody = request.Body
			_ = json.NewEncoder(w).Encode(map[string]any{"id": 17, "body": request.Body})
		default:
			t.Fatalf("unexpected method %s", r.Method)
		}
	}))
	defer server.Close()
	fixture.service.Provider = &gitlab.Client{BaseURL: server.URL, AdminToken: secretSentinel}
	command := sourceapp.PublishMergeRequestPlanSummaryCommand{
		TenantID: "tenant-1", ActorID: "agent-1", RepositoryID: fixture.repository.ID, ProviderIID: mergeRequest.ProviderIID,
		Plan: plan, Estimate: estimate, IdempotencyKey: "publish-plan-summary-1",
	}
	first, err := fixture.service.PublishMergeRequestPlanSummary(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	second, err := fixture.service.PublishMergeRequestPlanSummary(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	if first != second || first.ProviderNoteID != 17 || requests != 2 {
		t.Fatalf("first=%+v second=%+v requests=%d", first, second, requests)
	}
	for _, expected := range []string{"| CREATE | 1 |", "| UPDATE | 1 |", "| DELETE | 1 |", "RUB 125–750 minor units", "Approval: `required`", planHash} {
		if !strings.Contains(noteBody, expected) {
			t.Fatalf("summary missing %q: %s", expected, noteBody)
		}
	}
	if strings.Contains(noteBody, secretSentinel) || strings.Contains(noteBody, "twc_server.api") || strings.Contains(noteBody, "ProviderCost") {
		t.Fatalf("sensitive/provider detail leaked: %s", noteBody)
	}
}

func TestSource_GitLab429UsesBoundedRetryAndPreservesIdempotency(t *testing.T) {
	var createCalls, discoveryCalls, protectCalls, sleepCalls int
	correlationDescription := ""
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v4/projects":
			createCalls++
			var request struct {
				Description string `json:"description"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Fatal(err)
			}
			correlationDescription = request.Description
			w.Header().Set("Retry-After", "2")
			http.Error(w, "accepted but response rate limited", http.StatusTooManyRequests)
		case r.Method == http.MethodGet && r.URL.Path == "/api/v4/groups/10/projects":
			discoveryCalls++
			if discoveryCalls == 1 {
				w.Header().Set("Retry-After", "2")
				http.Error(w, "retry later", http.StatusTooManyRequests)
				return
			}
			_ = json.NewEncoder(w).Encode([]map[string]any{{
				"id": 42, "namespace": map[string]any{"id": 10}, "path": "rate-limited",
				"path_with_namespace": "beta/rate-limited", "web_url": "https://gitlab.example/beta/rate-limited",
				"default_branch": "main", "description": correlationDescription, "topics": []string{"ai-native-paas"},
			}})
		case r.Method == http.MethodPost && r.URL.Path == "/api/v4/projects/42/protected_branches":
			protectCalls++
			w.WriteHeader(http.StatusCreated)
		default:
			t.Fatalf("unexpected request %s %s?%s", r.Method, r.URL.Path, r.URL.RawQuery)
		}
	}))
	defer server.Close()
	provider := &gitlab.Client{
		BaseURL: server.URL, AdminToken: "gitlab-admin-sentinel", MaxRateLimitRetries: 2, MaxRateLimitDelay: 3 * time.Second,
		Sleep: func(_ context.Context, delay time.Duration) error {
			sleepCalls++
			if delay != 2*time.Second {
				t.Fatalf("unexpected retry delay %s", delay)
			}
			return nil
		},
	}
	store := memory.New()
	clock := &testkit.Clock{T: time.Date(2026, 7, 14, 15, 0, 0, 0, time.UTC)}
	ids := &testkit.IDs{}
	service := &sourceapp.Service{Store: store, Provider: provider, Clock: clock, IDs: ids}
	created, err := service.CreateProject(context.Background(), sourceapp.CreateProjectCommand{
		TenantID: "tenant-rate", ActorID: "user-rate", Name: "Rate limited", ProviderNamespaceID: 10, IdempotencyKey: "create-rate-limited",
	})
	if err != nil {
		t.Fatal(err)
	}
	ready, err := service.ProvisionRepository(context.Background(), sourceapp.ProvisionRepositoryCommand{TenantID: "tenant-rate", ActorID: "user-rate", RepositoryID: created.Repository.ID})
	if err != nil || ready.ProviderProjectID != 42 || ready.State != domain.RepositoryReady {
		t.Fatalf("ready=%+v err=%v", ready, err)
	}
	resumed, err := service.ProvisionRepository(context.Background(), sourceapp.ProvisionRepositoryCommand{TenantID: "tenant-rate", ActorID: "user-rate", RepositoryID: created.Repository.ID})
	if err != nil || resumed.ProviderProjectID != ready.ProviderProjectID {
		t.Fatalf("resumed=%+v err=%v", resumed, err)
	}
	if createCalls != 1 || discoveryCalls != 2 || protectCalls != 1 || sleepCalls != 1 {
		t.Fatalf("create=%d discovery=%d protect=%d sleeps=%d", createCalls, discoveryCalls, protectCalls, sleepCalls)
	}
	if !strings.Contains(correlationDescription, "paas-correlation") || strings.Contains(correlationDescription, provider.AdminToken) {
		t.Fatalf("unsafe correlation description %q", correlationDescription)
	}
}

func TestSource_ProjectArchiveIsTwoPhaseAndReversibleBeforePurge(t *testing.T) {
	fixture := newSourceV2Fixture(t)
	workspace, err := domain.NewWorkspace("workspace-archive", "tenant-1", fixture.repository.ID, "agent/archive", strings.Repeat("a", 40), fixture.clock.Now(), fixture.clock.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if err = workspace.MarkCloned("/workspace/archive", "credential-archive", fixture.clock.Now()); err != nil {
		t.Fatal(err)
	}
	if err = fixture.store.Transact(context.Background(), func(tx sourceapp.Tx) error { return tx.InsertWorkspace(workspace) }); err != nil {
		t.Fatal(err)
	}
	archiveCommand := sourceapp.ArchiveRepositoryCommand{TenantID: "tenant-1", ActorID: "user-1", RepositoryID: fixture.repository.ID, IdempotencyKey: "archive-1"}
	fixture.provider.ArchiveLostResponseOnce = true
	if _, err = fixture.service.ArchiveRepository(context.Background(), archiveCommand); !domain.HasCode(err, domain.CodeExternal) {
		t.Fatalf("lost archive response err=%v", err)
	}
	var gated domain.Repository
	if err = fixture.store.Transact(context.Background(), func(tx sourceapp.Tx) error {
		gated, _ = tx.GetRepository(fixture.repository.ID)
		return nil
	}); err != nil || gated.State != domain.RepositorySuspended {
		t.Fatalf("local source gate was not closed: repository=%+v err=%v", gated, err)
	}
	archived, err := fixture.service.ArchiveRepository(context.Background(), archiveCommand)
	if err != nil || archived.State != domain.RepositorySuspended || !fixture.provider.ArchivedProjects[fixture.repository.ProviderProjectID] || fixture.provider.RevokeCalls != 1 {
		t.Fatalf("archived=%+v revoke=%d err=%v", archived, fixture.provider.RevokeCalls, err)
	}
	replay, err := fixture.service.ArchiveRepository(context.Background(), archiveCommand)
	if err != nil || replay.State != domain.RepositorySuspended || fixture.provider.ArchiveCalls != 2 {
		t.Fatalf("archive replay=%+v calls=%d err=%v", replay, fixture.provider.ArchiveCalls, err)
	}
	workspaceService := &sourceapp.WorkspaceService{Store: fixture.store, Provider: fixture.provider, Git: &testkit.Git{}, Clock: fixture.clock, IDs: fixture.ids}
	if _, err = workspaceService.Execute(context.Background(), sourceapp.ExecuteWorkspaceCommand{TenantID: "tenant-1", ActorID: "agent-1", WorkspaceID: workspace.ID}); !domain.HasCode(err, domain.CodeNotFound) {
		t.Fatalf("archived workspace execution err=%v", err)
	}
	if _, err = workspaceService.Create(context.Background(), sourceapp.CreateWorkspaceCommand{TenantID: "tenant-1", ActorID: "agent-1", RepositoryID: fixture.repository.ID, Branch: "agent/new", BaseSHA: strings.Repeat("a", 40)}); !domain.HasCode(err, domain.CodeNotFound) {
		t.Fatalf("archived workspace create err=%v", err)
	}
	restored, err := fixture.service.RestoreRepository(context.Background(), sourceapp.RestoreRepositoryCommand{TenantID: "tenant-1", ActorID: "user-1", RepositoryID: fixture.repository.ID, IdempotencyKey: "restore-1"})
	if err != nil || restored.State != domain.RepositoryReady || restored.ProviderProjectID != archived.ProviderProjectID || restored.ProviderPath != archived.ProviderPath || fixture.provider.UnarchiveCalls != 1 {
		t.Fatalf("restored=%+v err=%v", restored, err)
	}
	if _, err = workspaceService.Create(context.Background(), sourceapp.CreateWorkspaceCommand{TenantID: "tenant-1", ActorID: "agent-1", RepositoryID: fixture.repository.ID, Branch: "agent/restored", BaseSHA: strings.Repeat("a", 40)}); err != nil {
		t.Fatalf("restored repository did not accept workspace: %v", err)
	}
	archived, err = fixture.service.ArchiveRepository(context.Background(), sourceapp.ArchiveRepositoryCommand{TenantID: "tenant-1", ActorID: "user-1", RepositoryID: fixture.repository.ID, IdempotencyKey: "archive-2"})
	if err != nil || archived.State != domain.RepositorySuspended {
		t.Fatalf("second archive=%+v err=%v", archived, err)
	}
	if _, err = fixture.service.PurgeRepository(context.Background(), sourceapp.PurgeRepositoryCommand{TenantID: "tenant-1", ActorID: "user-1", RepositoryID: fixture.repository.ID, ApprovalGrantID: "grant-purge-1", IdempotencyKey: "purge-1"}); !domain.HasCode(err, domain.CodeForbidden) || fixture.provider.DeleteCalls != 0 {
		t.Fatalf("purge without verifier err=%v deletes=%d", err, fixture.provider.DeleteCalls)
	}
	authorizer := &sourcePurgeAuthorizer{allowed: true}
	fixture.service.PurgeAuthorizer = authorizer
	purged, err := fixture.service.PurgeRepository(context.Background(), sourceapp.PurgeRepositoryCommand{TenantID: "tenant-1", ActorID: "user-1", RepositoryID: fixture.repository.ID, ApprovalGrantID: "grant-purge-1", IdempotencyKey: "purge-1"})
	if err != nil || purged.State != domain.RepositoryPurgePending || fixture.provider.DeleteCalls != 1 || authorizer.calls != 1 {
		t.Fatalf("purged=%+v deletes=%d auth=%d err=%v", purged, fixture.provider.DeleteCalls, authorizer.calls, err)
	}
	if authorizer.seen.RepositoryID != fixture.repository.ID || authorizer.seen.ProviderProjectID != fixture.repository.ProviderProjectID || authorizer.seen.ApprovalGrantID != "grant-purge-1" {
		t.Fatalf("authorization not exactly bound: %+v", authorizer.seen)
	}
	if _, err = fixture.service.RestoreRepository(context.Background(), sourceapp.RestoreRepositoryCommand{TenantID: "tenant-1", ActorID: "user-1", RepositoryID: fixture.repository.ID, IdempotencyKey: "restore-after-purge"}); !domain.HasCode(err, domain.CodeConflict) || fixture.provider.UnarchiveCalls != 1 {
		t.Fatalf("purge-pending restore err=%v unarchive=%d", err, fixture.provider.UnarchiveCalls)
	}
}

func TestSource_UnknownProviderProjectIsQuarantinedNotAdopted(t *testing.T) {
	fixture := newSourceV2Fixture(t)
	unknown := sourceapp.ProviderRepository{
		ID: 909, NamespaceID: fixture.repository.ProviderNamespaceID, Path: fixture.project.Slug,
		PathWithNamespace: "group-10/" + fixture.project.Slug, WebURL: "https://git.example/unmanaged",
		DefaultBranch: "main", Description: "customer-created project without platform identity",
	}
	fixture.provider.PutRepository(unknown)
	fixture.provider.PutRepository(sourceapp.ProviderRepository{
		ID: 910, NamespaceID: fixture.repository.ProviderNamespaceID, Path: "unrelated-customer-project",
		PathWithNamespace: "group-10/unrelated-customer-project", DefaultBranch: "main",
	})
	reconciler := sourceapp.Reconciler{Store: fixture.store, Provider: fixture.provider, Clock: fixture.clock, IDs: fixture.ids}
	first, err := reconciler.ReconcileAll(context.Background())
	if err != nil || first.Quarantines != 1 || first.QuarantineChanges != 1 || first.Errors != 0 {
		t.Fatalf("first reconciliation=%+v err=%v", first, err)
	}
	var quarantined domain.ProviderQuarantine
	var unrelatedFound bool
	var repository domain.Repository
	if err = fixture.store.Transact(context.Background(), func(tx sourceapp.Tx) error {
		quarantined, _ = tx.GetProviderQuarantine("gitlab", unknown.ID)
		_, unrelatedFound = tx.GetProviderQuarantine("gitlab", 910)
		repository, _ = tx.GetRepository(fixture.repository.ID)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if quarantined.ProviderProjectID != unknown.ID || quarantined.CandidateRepositoryID != fixture.repository.ID || quarantined.Reason != domain.QuarantineExternalIdentityMismatch || quarantined.ExternalIdentityMatched || quarantined.ManagedLabelPresent {
		t.Fatalf("quarantine=%+v", quarantined)
	}
	if unrelatedFound || repository.ProviderProjectID != fixture.repository.ProviderProjectID {
		t.Fatalf("unrelated=%v repository was adopted=%+v", unrelatedFound, repository)
	}
	events := sourceV2Outbox(fixture.store, "source.provider_project_quarantined.v2")
	if len(events) != 1 || strings.Contains(string(events[0].Payload), "tenant_id") {
		t.Fatalf("quarantine events=%+v", events)
	}
	firstObserved := quarantined.FirstObservedAt
	fixture.clock.Advance(time.Minute)
	second, err := reconciler.ReconcileAll(context.Background())
	if err != nil || second.Quarantines != 1 || second.QuarantineChanges != 0 || len(sourceV2Outbox(fixture.store, "source.provider_project_quarantined.v2")) != 1 {
		t.Fatalf("second reconciliation=%+v err=%v", second, err)
	}
	if err = fixture.store.Transact(context.Background(), func(tx sourceapp.Tx) error {
		quarantined, _ = tx.GetProviderQuarantine("gitlab", unknown.ID)
		return nil
	}); err != nil || !quarantined.FirstObservedAt.Equal(firstObserved) || !quarantined.LastObservedAt.After(firstObserved) {
		t.Fatalf("quarantine observation=%+v err=%v", quarantined, err)
	}
}
