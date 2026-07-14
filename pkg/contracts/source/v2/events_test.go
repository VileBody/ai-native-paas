package v2_test

import (
	"strings"
	"testing"
	"time"

	sourcev2 "github.com/keir-research/ai-native-paas/pkg/contracts/source/v2"
)

func TestRevisionObservedEvent_RequiresImmutableRevisionAndBoundedReason(t *testing.T) {
	event := sourcev2.RevisionObservedEvent{
		TenantID: "tenant-1", ProjectID: "project-1", RepositoryID: "repo-1", Branch: "main",
		CommitSHA: strings.Repeat("a", 40), Reason: "reconciliation", ObservedAt: time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC),
	}
	if err := event.Validate(); err != nil {
		t.Fatal(err)
	}
	event.Reason = "bootstrap"
	if err := event.Validate(); err != nil {
		t.Fatalf("bounded bootstrap reason rejected: %v", err)
	}
	event.CommitSHA = "main"
	if err := event.Validate(); err == nil {
		t.Fatal("mutable revision accepted")
	}
	event.CommitSHA = strings.Repeat("a", 40)
	event.Reason = "provider-payload-defined"
	if err := event.Validate(); err == nil {
		t.Fatal("unbounded reason accepted")
	}
}

func TestEnvironmentCleanupRequestedEvent_RequiresExactMappedIdentity(t *testing.T) {
	event := sourcev2.EnvironmentCleanupRequestedEvent{
		TenantID: "tenant-1", ProjectID: "project-1", RepositoryID: "repo-1", Branch: "preview/mr-1",
		EnvironmentID: "env-preview-1", Reason: "branch_deleted", ProviderEventID: "gitlab-event-1",
		RequestedAt: time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC),
	}
	if err := event.Validate(); err != nil {
		t.Fatal(err)
	}
	event.EnvironmentID = ""
	if err := event.Validate(); err == nil {
		t.Fatal("cleanup without mapped environment accepted")
	}
}
