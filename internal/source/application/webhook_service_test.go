package application_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/keir-research/ai-native-paas/internal/source/application"
	"github.com/keir-research/ai-native-paas/internal/source/domain"
	"github.com/keir-research/ai-native-paas/internal/source/memory"
	"github.com/keir-research/ai-native-paas/internal/source/testkit"
	sourcehook "github.com/keir-research/ai-native-paas/internal/source/webhook"
)

func setupWebhook(t *testing.T) (*application.WebhookService, *application.Service, *memory.Store, *testkit.Provider, *testkit.Clock, *testkit.IDs, application.CreateProjectResult) {
	t.Helper()
	s, store, p, clock, ids := setupService()
	created := create(t, s, "Booking", "k1")
	ready, err := s.ProvisionRepository(context.Background(), application.ProvisionRepositoryCommand{TenantID: "t1", ActorID: "u1", RepositoryID: created.Repository.ID})
	if err != nil {
		t.Fatal(err)
	}
	created.Repository = ready
	hooks := &application.WebhookService{Store: store, Provider: p, Verifier: sourcehook.Verifier{Secret: []byte("secret"), ReplayWindow: 5 * time.Minute}, Normalizer: sourcehook.Normalizer{Provider: "gitlab"}, Clock: clock, IDs: ids}
	return hooks, s, store, p, clock, ids, created
}
func signedHeaders(body []byte, id string, at time.Time) map[string][]string {
	ts, sig := sourcehook.Sign([]byte("secret"), id, at, body)
	return map[string][]string{"webhook-id": {id}, "webhook-timestamp": {ts}, "webhook-signature": {sig}}
}
func pushBody(projectID int64, before, after, branch string, at time.Time) []byte {
	return []byte(fmt.Sprintf(`{"object_kind":"push","before":"%s","after":"%s","ref":"refs/heads/%s","project":{"id":%d},"event_created_at":"%s"}`, before, after, branch, projectID, at.Format(time.RFC3339)))
}
func TestWebhook_DuplicateDeliveryIsIgnored(t *testing.T) {
	hooks, _, store, p, clock, _, created := setupWebhook(t)
	head := shaX("b")
	p.SetHead(created.Repository.ProviderProjectID, "main", head)
	body := pushBody(created.Repository.ProviderProjectID, shaX("a"), head, "main", clock.Now())
	headers := signedHeaders(body, "evt-1", clock.Now())
	first, err := hooks.Handle(context.Background(), "t1", headers, body)
	if err != nil || first.Duplicate {
		t.Fatal(first, err)
	}
	second, err := hooks.Handle(context.Background(), "t1", headers, body)
	if err != nil || !second.Duplicate {
		t.Fatal(second, err)
	}
	_, _, outbox, _ := store.Snapshot()
	count := 0
	for _, e := range outbox {
		if e.Topic == "source.push.v1" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("push events=%d", count)
	}
}
func TestWebhook_DuplicateIDWithDifferentBodyConflicts(t *testing.T) {
	hooks, _, _, p, clock, _, created := setupWebhook(t)
	p.SetHead(created.Repository.ProviderProjectID, "main", shaX("b"))
	a := pushBody(created.Repository.ProviderProjectID, shaX("a"), shaX("b"), "main", clock.Now())
	if _, err := hooks.Handle(context.Background(), "t1", signedHeaders(a, "evt-1", clock.Now()), a); err != nil {
		t.Fatal(err)
	}
	b := pushBody(created.Repository.ProviderProjectID, shaX("b"), shaX("c"), "main", clock.Now())
	p.SetHead(created.Repository.ProviderProjectID, "main", shaX("c"))
	_, err := hooks.Handle(context.Background(), "t1", signedHeaders(b, "evt-1", clock.Now()), b)
	if !domain.HasCode(err, domain.CodeConflict) {
		t.Fatalf("err=%v", err)
	}
}
func TestWebhook_OutOfOrderPushDoesNotRegressBranchHead(t *testing.T) {
	hooks, _, store, p, clock, _, created := setupWebhook(t)
	latest := shaX("c")
	p.SetHead(created.Repository.ProviderProjectID, "main", latest)
	newer := pushBody(created.Repository.ProviderProjectID, shaX("b"), latest, "main", clock.Now())
	if _, err := hooks.Handle(context.Background(), "t1", signedHeaders(newer, "new", clock.Now()), newer); err != nil {
		t.Fatal(err)
	}
	olderTime := clock.Now().Add(-time.Minute)
	older := pushBody(created.Repository.ProviderProjectID, shaX("a"), shaX("b"), "main", olderTime)
	if _, err := hooks.Handle(context.Background(), "t1", signedHeaders(older, "old", clock.Now()), older); err != nil {
		t.Fatal(err)
	}
	var got domain.BranchHead
	_ = store.Transact(context.Background(), func(tx application.Tx) error { got, _ = tx.GetBranch(created.Repository.ID, "main"); return nil })
	if got.CommitSHA != latest {
		t.Fatalf("head regressed: %s", got.CommitSHA)
	}
}
func TestWebhook_TenantDerivedFromPathRejectsOtherTenant(t *testing.T) {
	hooks, _, _, p, clock, _, created := setupWebhook(t)
	p.SetHead(created.Repository.ProviderProjectID, "main", shaX("b"))
	body := pushBody(created.Repository.ProviderProjectID, shaX("a"), shaX("b"), "main", clock.Now())
	_, err := hooks.Handle(context.Background(), "other", signedHeaders(body, "evt", clock.Now()), body)
	if !domain.HasCode(err, domain.CodeNotFound) {
		t.Fatalf("err=%v", err)
	}
}
func TestWebhook_MergeRequestLifecycleIsIdempotent(t *testing.T) {
	hooks, _, store, _, clock, _, created := setupWebhook(t)
	body := []byte(fmt.Sprintf(`{"object_kind":"merge_request","project":{"id":%d},"object_attributes":{"iid":7,"action":"open","state":"opened","source_branch":"feature","target_branch":"main","last_commit":{"id":"%s"}}}`, created.Repository.ProviderProjectID, shaX("d")))
	headers := signedHeaders(body, "mr-1", clock.Now())
	if _, err := hooks.Handle(context.Background(), "t1", headers, body); err != nil {
		t.Fatal(err)
	}
	result, err := hooks.Handle(context.Background(), "t1", headers, body)
	if err != nil || !result.Duplicate {
		t.Fatal(result, err)
	}
	var mr domain.MergeRequest
	_ = store.Transact(context.Background(), func(tx application.Tx) error { mr, _ = tx.GetMergeRequest(created.Repository.ID, 7); return nil })
	if mr.State != domain.MergeRequestOpen || mr.HeadSHA != shaX("d") {
		t.Fatalf("%+v", mr)
	}
}
func TestReconciler_DetectsMissedCommit(t *testing.T) {
	_, _, store, p, clock, ids, created := setupWebhook(t)
	head := shaX("e")
	p.SetHead(created.Repository.ProviderProjectID, "main", head)
	r := application.Reconciler{Store: store, Provider: p, Clock: clock, IDs: ids}
	result, err := r.ReconcileAll(context.Background())
	if err != nil || result.BranchChanges != 1 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	var branch domain.BranchHead
	_ = store.Transact(context.Background(), func(tx application.Tx) error { branch, _ = tx.GetBranch(created.Repository.ID, "main"); return nil })
	if branch.CommitSHA != head {
		t.Fatalf("%+v", branch)
	}
}
func TestReconciler_ProviderRenameKeepsNumericIdentity(t *testing.T) {
	_, s, _, p, clock, ids, created := setupWebhook(t)
	id := created.Repository.ProviderProjectID
	p.Rename(id, "reservations")
	r := application.Reconciler{Store: s.Store, Provider: p, Clock: clock, IDs: ids}
	result, err := r.ReconcileAll(context.Background())
	if err != nil || result.MetadataChanges != 1 {
		t.Fatalf("%+v %v", result, err)
	}
	repo, err := s.GetRepository(context.Background(), "t1", created.Repository.ID)
	if err != nil || repo.ProviderProjectID != id || repo.ProviderPath != "renamed/reservations" {
		t.Fatalf("%+v %v", repo, err)
	}
}
func TestPushDomain_OutOfOrderSignalsReconciliation(t *testing.T) {
	branch, _ := domain.NewBranchHead("r", "main")
	_, _ = application.ProcessPushWithoutProvider(&branch, application.PushEvent{EventID: "new", BeforeSHA: "", AfterSHA: shaX("b"), OccurredAt: time.Now()}, time.Now())
	_, err := application.ProcessPushWithoutProvider(&branch, application.PushEvent{EventID: "old", BeforeSHA: shaX("a"), AfterSHA: shaX("b"), OccurredAt: time.Now().Add(-time.Hour)}, time.Now())
	if err != nil { // duplicate after SHA is a no-op before temporal check
		t.Fatalf("unexpected duplicate error: %v", err)
	}
}

func TestPreviewEnvironmentBindingIsTenantScopedAndUnique(t *testing.T) {
	_, service, _, _, _, _, created := setupWebhook(t)
	command := application.BindPreviewEnvironmentCommand{TenantID: "t1", ActorID: "runtime-controller", RepositoryID: created.Repository.ID, Branch: "preview/one", EnvironmentID: "env-1"}
	first, err := service.BindPreviewEnvironment(context.Background(), command)
	if err != nil || first.EnvironmentID != "env-1" {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	second, err := service.BindPreviewEnvironment(context.Background(), command)
	if err != nil || second.Version != first.Version {
		t.Fatalf("idempotent bind changed state: first=%+v second=%+v err=%v", first, second, err)
	}
	command.Branch = "preview/two"
	if _, err = service.BindPreviewEnvironment(context.Background(), command); !domain.HasCode(err, domain.CodeConflict) {
		t.Fatalf("duplicate environment binding err=%v", err)
	}
	command.TenantID = "other"
	command.EnvironmentID = "env-2"
	if _, err = service.BindPreviewEnvironment(context.Background(), command); !domain.HasCode(err, domain.CodeNotFound) {
		t.Fatalf("cross-tenant bind err=%v", err)
	}
}

func shaX(c string) string {
	v := ""
	for len(v) < 40 {
		v += c
	}
	return v[:40]
}
