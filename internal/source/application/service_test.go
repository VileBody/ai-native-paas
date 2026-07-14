package application_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/keir-research/ai-native-paas/internal/source/application"
	"github.com/keir-research/ai-native-paas/internal/source/domain"
	"github.com/keir-research/ai-native-paas/internal/source/memory"
	"github.com/keir-research/ai-native-paas/internal/source/testkit"
)

func setupService() (*application.Service, *memory.Store, *testkit.Provider, *testkit.Clock, *testkit.IDs) {
	store := memory.New()
	provider := testkit.NewProvider()
	clock := &testkit.Clock{T: time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)}
	ids := &testkit.IDs{}
	return &application.Service{Store: store, Provider: provider, Clock: clock, IDs: ids}, store, provider, clock, ids
}
func create(t *testing.T, s *application.Service, name, key string) application.CreateProjectResult {
	t.Helper()
	r, err := s.CreateProject(context.Background(), application.CreateProjectCommand{TenantID: "t1", ActorID: "u1", Name: name, ProviderNamespaceID: 10, IdempotencyKey: key})
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func TestProjectCreate_WritesProjectRepositoryOutboxAndAudit(t *testing.T) {
	s, store, _, _, _ := setupService()
	r := create(t, s, "Booking", "k1")
	projects, repos, outbox, audit := store.Snapshot()
	if len(projects) != 1 || len(repos) != 1 || len(outbox) != 1 || len(audit) != 1 || r.Repository.ProjectID != r.Project.ID {
		t.Fatalf("p=%d r=%d o=%d a=%d result=%+v", len(projects), len(repos), len(outbox), len(audit), r)
	}
}
func TestProjectCreate_SameIdempotencyReturnsOriginalResult(t *testing.T) {
	s, store, _, _, _ := setupService()
	a := create(t, s, "Booking", "same")
	b := create(t, s, "Booking", "same")
	projects, _, _, _ := store.Snapshot()
	if a.Project.ID != b.Project.ID || len(projects) != 1 {
		t.Fatalf("a=%+v b=%+v count=%d", a, b, len(projects))
	}
}
func TestProjectCreate_SameKeyDifferentPayloadConflicts(t *testing.T) {
	s, _, _, _, _ := setupService()
	_ = create(t, s, "Booking", "same")
	_, err := s.CreateProject(context.Background(), application.CreateProjectCommand{TenantID: "t1", ActorID: "u1", Name: "Other", ProviderNamespaceID: 10, IdempotencyKey: "same"})
	if !domain.HasCode(err, domain.CodeConflict) {
		t.Fatalf("err=%v", err)
	}
}
func TestProjectCreate_ConcurrentSameKeyCreatesOneProject(t *testing.T) {
	s, store, _, _, _ := setupService()
	const n = 24
	var wg sync.WaitGroup
	errs := make(chan error, n)
	ids := make(chan string, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := s.CreateProject(context.Background(), application.CreateProjectCommand{TenantID: "t1", ActorID: "u1", Name: "Booking", ProviderNamespaceID: 10, IdempotencyKey: "same"})
			if err == nil {
				ids <- r.Project.ID
			}
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	close(ids)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	first := ""
	for id := range ids {
		if first == "" {
			first = id
		}
		if id != first {
			t.Fatalf("different IDs: %q vs %q", first, id)
		}
	}
	projects, _, _, _ := store.Snapshot()
	if len(projects) != 1 {
		t.Fatalf("projects=%d", len(projects))
	}
}
func TestProjectCreate_TenantScopedSlugUniqueness(t *testing.T) {
	s, _, _, _, _ := setupService()
	_ = create(t, s, "Booking", "k1")
	_, err := s.CreateProject(context.Background(), application.CreateProjectCommand{TenantID: "t1", ActorID: "u1", Name: "booking", ProviderNamespaceID: 10, IdempotencyKey: "k2"})
	if !domain.HasCode(err, domain.CodeConflict) {
		t.Fatalf("err=%v", err)
	}
}
func TestProjectRead_DeniesCrossTenantByNonDisclosure(t *testing.T) {
	s, _, _, _, _ := setupService()
	r := create(t, s, "Booking", "k1")
	_, err := s.GetProject(context.Background(), "other", r.Project.ID)
	if !domain.HasCode(err, domain.CodeNotFound) {
		t.Fatalf("err=%v", err)
	}
}
func TestRepository_ProvisioningIsIdempotent(t *testing.T) {
	s, _, provider, _, _ := setupService()
	r := create(t, s, "Booking", "k1")
	a, err := s.ProvisionRepository(context.Background(), application.ProvisionRepositoryCommand{TenantID: "t1", ActorID: "u1", RepositoryID: r.Repository.ID})
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.ProvisionRepository(context.Background(), application.ProvisionRepositoryCommand{TenantID: "t1", ActorID: "u1", RepositoryID: r.Repository.ID})
	if err != nil {
		t.Fatal(err)
	}
	if a.ProviderProjectID != b.ProviderProjectID || provider.CreateCalls != 1 {
		t.Fatalf("a=%+v b=%+v calls=%d", a, b, provider.CreateCalls)
	}
}
func TestRepository_LostCreateResponseRecoversByCorrelation(t *testing.T) {
	s, _, provider, _, _ := setupService()
	provider.LostResponseOnce = true
	r := create(t, s, "Booking", "k1")
	ready, err := s.ProvisionRepository(context.Background(), application.ProvisionRepositoryCommand{TenantID: "t1", ActorID: "u1", RepositoryID: r.Repository.ID})
	if err != nil || ready.State != domain.RepositoryReady || provider.CreateCalls != 1 {
		t.Fatalf("repo=%+v calls=%d err=%v", ready, provider.CreateCalls, err)
	}
}
func TestRepository_ProtectionFailureDoesNotPublishReady(t *testing.T) {
	s, store, provider, _, _ := setupService()
	provider.ProtectError = errors.New("denied")
	r := create(t, s, "Booking", "k1")
	_, err := s.ProvisionRepository(context.Background(), application.ProvisionRepositoryCommand{TenantID: "t1", ActorID: "u1", RepositoryID: r.Repository.ID})
	if err == nil {
		t.Fatal("expected error")
	}
	_, _, outbox, _ := store.Snapshot()
	for _, e := range outbox {
		if e.Topic == "source.repository_ready.v1" {
			t.Fatal("ready event published")
		}
	}
}
func TestProjectRename_DoesNotMutateNumericProviderIdentity(t *testing.T) {
	s, _, _, _, _ := setupService()
	r := create(t, s, "Booking", "k1")
	ready, err := s.ProvisionRepository(context.Background(), application.ProvisionRepositoryCommand{TenantID: "t1", ActorID: "u1", RepositoryID: r.Repository.ID})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.RenameProject(context.Background(), "t1", "u1", r.Project.ID, "Reservations")
	if err != nil {
		t.Fatal(err)
	}
	after, err := s.GetRepository(context.Background(), "t1", r.Repository.ID)
	if err != nil || after.ProviderProjectID != ready.ProviderProjectID {
		t.Fatalf("before=%d after=%d err=%v", ready.ProviderProjectID, after.ProviderProjectID, err)
	}
}

func TestSource_CreateMergeRequestBindsExactRemoteHeadAndIsIdempotent(t *testing.T) {
	s, store, provider, _, _ := setupService()
	created := create(t, s, "Booking", "project-1")
	repository, err := s.ProvisionRepository(context.Background(), application.ProvisionRepositoryCommand{TenantID: "t1", ActorID: "u1", RepositoryID: created.Repository.ID})
	if err != nil {
		t.Fatal(err)
	}
	head := strings.Repeat("b", 40)
	provider.SetHead(repository.ProviderProjectID, "agent/task-1", head)
	command := application.CreateMergeRequestCommand{
		TenantID: "t1", ActorID: "agent-1", RepositoryID: repository.ID,
		SourceBranch: "agent/task-1", TargetBranch: "main", ExpectedHeadSHA: head,
		SourcePlanHash: "sha256:" + strings.Repeat("c", 64), TaskID: "task-1", CorrelationID: "corr-1",
		Title: "Implement governed change", IdempotencyKey: "mr-1",
	}
	first, err := s.CreateMergeRequest(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.CreateMergeRequest(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	if first.ProviderIID != second.ProviderIID || first.HeadSHA != head || first.TargetBranch != repository.DefaultBranch || provider.MergeRequestCalls != 1 {
		t.Fatalf("first=%+v second=%+v calls=%d", first, second, provider.MergeRequestCalls)
	}
	if provider.LastMergeRequest.ProjectID != repository.ProviderProjectID || !strings.Contains(provider.LastMergeRequest.Description, command.SourcePlanHash) || strings.Contains(provider.LastMergeRequest.Description, provider.Token) {
		t.Fatalf("provider request was not governed: %+v", provider.LastMergeRequest)
	}
	_, _, outbox, audit := store.Snapshot()
	if outbox[len(outbox)-1].Topic != "source.merge_request_created.v2" || audit[len(audit)-1].Action != "source.merge_request.create" || strings.Contains(string(audit[len(audit)-1].Data), provider.Token) {
		t.Fatalf("outbox=%+v audit=%+v", outbox, audit)
	}
}

func TestSource_CreateMergeRequestRejectsAttestedHeadDriftBeforeProviderMutation(t *testing.T) {
	s, _, provider, _, _ := setupService()
	created := create(t, s, "Booking", "project-1")
	repository, err := s.ProvisionRepository(context.Background(), application.ProvisionRepositoryCommand{TenantID: "t1", ActorID: "u1", RepositoryID: created.Repository.ID})
	if err != nil {
		t.Fatal(err)
	}
	provider.SetHead(repository.ProviderProjectID, "agent/task-1", strings.Repeat("d", 40))
	_, err = s.CreateMergeRequest(context.Background(), application.CreateMergeRequestCommand{
		TenantID: "t1", ActorID: "agent-1", RepositoryID: repository.ID,
		SourceBranch: "agent/task-1", TargetBranch: "main", ExpectedHeadSHA: strings.Repeat("b", 40),
		SourcePlanHash: "sha256:" + strings.Repeat("c", 64), TaskID: "task-1", CorrelationID: "corr-1",
		Title: "Implement governed change", IdempotencyKey: "mr-drift",
	})
	if !domain.HasCode(err, domain.CodeConflict) || provider.MergeRequestCalls != 0 {
		t.Fatalf("err=%v calls=%d", err, provider.MergeRequestCalls)
	}
}

func TestSource_CreateMergeRequestRecoversLostProviderResponseWithoutDuplicate(t *testing.T) {
	s, _, provider, _, _ := setupService()
	created := create(t, s, "Booking", "project-1")
	repository, err := s.ProvisionRepository(context.Background(), application.ProvisionRepositoryCommand{TenantID: "t1", ActorID: "u1", RepositoryID: created.Repository.ID})
	if err != nil {
		t.Fatal(err)
	}
	head := strings.Repeat("b", 40)
	provider.SetHead(repository.ProviderProjectID, "agent/task-1", head)
	provider.MergeRequestLostResponseOnce = true
	result, err := s.CreateMergeRequest(context.Background(), application.CreateMergeRequestCommand{
		TenantID: "t1", ActorID: "agent-1", RepositoryID: repository.ID,
		SourceBranch: "agent/task-1", TargetBranch: "main", ExpectedHeadSHA: head,
		SourcePlanHash: "sha256:" + strings.Repeat("c", 64), TaskID: "task-1", CorrelationID: "corr-1",
		Title: "Implement governed change", IdempotencyKey: "mr-lost",
	})
	if err != nil || result.ProviderIID == 0 || provider.MergeRequestCalls != 1 {
		t.Fatalf("result=%+v err=%v calls=%d", result, err, provider.MergeRequestCalls)
	}
}

func TestSource_CreateMergeRequestConcurrentRetryConvergesOnOneProviderMR(t *testing.T) {
	s, _, provider, _, _ := setupService()
	created := create(t, s, "Booking", "project-1")
	repository, err := s.ProvisionRepository(context.Background(), application.ProvisionRepositoryCommand{TenantID: "t1", ActorID: "u1", RepositoryID: created.Repository.ID})
	if err != nil {
		t.Fatal(err)
	}
	head := strings.Repeat("b", 40)
	provider.SetHead(repository.ProviderProjectID, "agent/task-1", head)
	command := application.CreateMergeRequestCommand{
		TenantID: "t1", ActorID: "agent-1", RepositoryID: repository.ID,
		SourceBranch: "agent/task-1", TargetBranch: "main", ExpectedHeadSHA: head,
		SourcePlanHash: "sha256:" + strings.Repeat("c", 64), TaskID: "task-1", CorrelationID: "corr-1",
		Title: "Implement governed change", IdempotencyKey: "mr-concurrent",
	}
	const invocations = 16
	var wait sync.WaitGroup
	errorsFound := make(chan error, invocations)
	providerIIDs := make(chan int64, invocations)
	for index := 0; index < invocations; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			result, callErr := s.CreateMergeRequest(context.Background(), command)
			errorsFound <- callErr
			providerIIDs <- result.ProviderIID
		}()
	}
	wait.Wait()
	close(errorsFound)
	close(providerIIDs)
	for callErr := range errorsFound {
		if callErr != nil {
			t.Fatal(callErr)
		}
	}
	for iid := range providerIIDs {
		if iid != 1 {
			t.Fatalf("provider iid=%d", iid)
		}
	}
	if provider.MergeRequestCalls != 1 {
		t.Fatalf("provider create calls=%d", provider.MergeRequestCalls)
	}
}
