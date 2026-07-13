package application_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/keir-research/ai-native-paas/internal/source/application"
	"github.com/keir-research/ai-native-paas/internal/source/domain"
	"github.com/keir-research/ai-native-paas/internal/source/testkit"
)

func setupWorkspace(t *testing.T) (*application.WorkspaceService, *testkit.Provider, *testkit.Git, *testkit.Clock, application.CreateProjectResult) {
	t.Helper()
	source, store, provider, clock, ids := setupService()
	created := create(t, source, "Booking", "k1")
	ready, err := source.ProvisionRepository(context.Background(), application.ProvisionRepositoryCommand{TenantID: "t1", ActorID: "u1", RepositoryID: created.Repository.ID})
	if err != nil {
		t.Fatal(err)
	}
	created.Repository = ready
	git := &testkit.Git{}
	ws := &application.WorkspaceService{Store: store, Provider: provider, Git: git, Clock: clock, IDs: ids}
	return ws, provider, git, clock, created
}
func TestWorkspaceService_ExecuteCompletesOnce(t *testing.T) {
	s, p, g, _, created := setupWorkspace(t)
	w, err := s.Create(context.Background(), application.CreateWorkspaceCommand{TenantID: "t1", ActorID: "u1", RepositoryID: created.Repository.ID, Branch: "feature", BaseSHA: shaX("a"), TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	w, err = s.Execute(context.Background(), application.ExecuteWorkspaceCommand{TenantID: "t1", ActorID: "u1", WorkspaceID: w.ID, RemoteURL: "https://git/repo.git", Message: "change", Patch: []application.PatchOperation{{Path: "x", Content: []byte("x")}}})
	if err != nil || w.State != domain.WorkspaceCompleted || g.PushCalls != 1 || p.RevokeCalls != 1 {
		t.Fatalf("w=%+v push=%d revoke=%d err=%v", w, g.PushCalls, p.RevokeCalls, err)
	}
	again, err := s.Execute(context.Background(), application.ExecuteWorkspaceCommand{TenantID: "t1", ActorID: "u1", WorkspaceID: w.ID})
	if err != nil || again.State != domain.WorkspaceCompleted || g.PushCalls != 1 {
		t.Fatalf("again=%+v pushes=%d err=%v", again, g.PushCalls, err)
	}
}
func TestWorkspaceService_RevokeFailureRetriesWithoutSecondPush(t *testing.T) {
	s, p, g, _, created := setupWorkspace(t)
	p.RevokeError = errors.New("temporary revoke failure")
	w, _ := s.Create(context.Background(), application.CreateWorkspaceCommand{TenantID: "t1", ActorID: "u1", RepositoryID: created.Repository.ID, Branch: "feature", BaseSHA: shaX("a"), TTL: time.Hour})
	w, err := s.Execute(context.Background(), application.ExecuteWorkspaceCommand{TenantID: "t1", ActorID: "u1", WorkspaceID: w.ID, RemoteURL: "x", Message: "change", Patch: []application.PatchOperation{{Path: "x", Content: []byte("x")}}})
	if err == nil || w.State != domain.WorkspacePushed || g.PushCalls != 1 {
		t.Fatalf("w=%+v pushes=%d err=%v", w, g.PushCalls, err)
	}
	p.RevokeError = nil
	w, err = s.Execute(context.Background(), application.ExecuteWorkspaceCommand{TenantID: "t1", ActorID: "u1", WorkspaceID: w.ID})
	if err != nil || w.State != domain.WorkspaceCompleted || g.PushCalls != 1 || p.RevokeCalls != 2 {
		t.Fatalf("w=%+v pushes=%d revokes=%d err=%v", w, g.PushCalls, p.RevokeCalls, err)
	}
}
func TestWorkspaceService_ExpiredWorkspaceCanBeCleanedUp(t *testing.T) {
	s, _, g, clock, created := setupWorkspace(t)
	w, _ := s.Create(context.Background(), application.CreateWorkspaceCommand{TenantID: "t1", ActorID: "u1", RepositoryID: created.Repository.ID, Branch: "feature", BaseSHA: shaX("a"), TTL: time.Minute})
	clock.Advance(2 * time.Minute)
	w, err := s.Delete(context.Background(), "t1", w.ID)
	if err != nil || w.State != domain.WorkspaceDeleted || g.CleanupCalls != 1 {
		t.Fatalf("w=%+v cleanup=%d err=%v", w, g.CleanupCalls, err)
	}
}
func TestWorkspaceService_CrossTenantWorkspaceIsNotDisclosed(t *testing.T) {
	s, _, _, _, created := setupWorkspace(t)
	w, _ := s.Create(context.Background(), application.CreateWorkspaceCommand{TenantID: "t1", ActorID: "u1", RepositoryID: created.Repository.ID, Branch: "feature", BaseSHA: shaX("a"), TTL: time.Hour})
	_, err := s.Execute(context.Background(), application.ExecuteWorkspaceCommand{TenantID: "other", WorkspaceID: w.ID})
	if !domain.HasCode(err, domain.CodeNotFound) {
		t.Fatalf("err=%v", err)
	}
}
