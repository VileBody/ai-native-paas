package application_test

import (
	"context"
	"errors"
	"os/exec"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/keir-research/ai-native-paas/internal/runtime/application"
	"github.com/keir-research/ai-native-paas/internal/runtime/domain"
	"github.com/keir-research/ai-native-paas/internal/runtime/gitops"
	runtimev1 "github.com/keir-research/ai-native-paas/pkg/contracts/runtime/v1"
)

func useLocalGitOps(t *testing.T, f *fixture) *gitops.LocalRepository {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	repo, err := gitops.NewLocalRepository(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f.service.GitOps = repo
	return repo
}

func TestGitOpsCommit_DBFailureAfterPushIsRecoveredByReconciler(t *testing.T) {
	f := newFixture(t)
	useLocalGitOps(t, f)
	var once atomic.Bool
	f.service.AfterGitPush = func() error {
		if once.CompareAndSwap(false, true) {
			return errors.New("simulated database outage after git push")
		}
		return nil
	}
	req := f.request("git-recovery")
	_, err := f.service.Deploy(context.Background(), req)
	if err == nil {
		t.Fatal("expected injected failure")
	}
	snapshot := f.store.Snapshot()
	if len(snapshot.Commits) != 0 {
		t.Fatalf("commit was recorded despite injected failure: %#v", snapshot.Commits)
	}
	var releaseID string
	for id := range snapshot.Releases {
		releaseID = id
	}
	if releaseID == "" {
		t.Fatal("release was not created")
	}
	if err := f.service.ReconcileGitOps(context.Background(), releaseID, "reconciler"); err != nil {
		t.Fatal(err)
	}
	snapshot = f.store.Snapshot()
	commit, ok := snapshot.Commits[releaseID]
	if !ok || commit.CommitSHA == "" {
		t.Fatalf("Git commit was not recovered: %#v", snapshot.Commits)
	}
	deployment := snapshot.Deployments[commit.DeploymentID]
	if deployment.Phase != runtimev1.DeploymentGitCommitted || deployment.GitCommitSHA != commit.CommitSHA {
		t.Fatalf("deployment=%+v commit=%+v", deployment, commit)
	}
	if snapshot.Releases[releaseID].State != runtimev1.ReleaseCommittedToGitOps {
		t.Fatalf("release=%+v", snapshot.Releases[releaseID])
	}
}

func TestGitOpsCommit_ConcurrentDeployUsesOptimisticLock(t *testing.T) {
	f := newFixture(t)
	useLocalGitOps(t, f)
	const workers = 12
	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	refs := make(chan runtimev1.DeploymentRef, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			req := f.request("concurrent-deploy")
			ref, err := f.service.Deploy(context.Background(), req)
			if err != nil {
				errs <- err
				return
			}
			refs <- ref
		}(i)
	}
	close(start)
	wg.Wait()
	close(errs)
	close(refs)
	for err := range errs {
		t.Errorf("deploy failed: %v", err)
	}
	if t.Failed() {
		return
	}
	var releaseID, deploymentID string
	for ref := range refs {
		if releaseID == "" {
			releaseID, deploymentID = ref.ReleaseID, ref.DeploymentID
		}
		if ref.ReleaseID != releaseID || ref.DeploymentID != deploymentID {
			t.Fatalf("multiple winners: first=%s/%s got=%s/%s", releaseID, deploymentID, ref.ReleaseID, ref.DeploymentID)
		}
	}
	snapshot := f.store.Snapshot()
	if len(snapshot.Releases) != 1 || len(snapshot.Deployments) != 1 || len(snapshot.Commits) != 1 {
		t.Fatalf("snapshot releases=%d deployments=%d commits=%d", len(snapshot.Releases), len(snapshot.Deployments), len(snapshot.Commits))
	}
}

func TestGitOpsCommit_RevertCreatesExplicitRollbackRelease(t *testing.T) {
	f := newFixture(t)
	useLocalGitOps(t, f)
	original, err := f.service.Deploy(context.Background(), f.request("deploy-original"))
	if err != nil {
		t.Fatal(err)
	}
	rollback, err := f.service.RollbackWithRequest(context.Background(), application.RollbackRequest{TenantID: f.app.TenantID, EnvironmentID: f.env.ID, TargetReleaseID: original.ReleaseID, ActorID: "user-1", IdempotencyKey: "rollback-1"})
	if err != nil {
		t.Fatal(err)
	}
	if rollback.ReleaseID == original.ReleaseID {
		t.Fatal("rollback reused old release identity instead of creating an auditable release")
	}
	snapshot := f.store.Snapshot()
	candidate := snapshot.Releases[rollback.ReleaseID]
	target := snapshot.Releases[original.ReleaseID]
	if candidate.RollbackOf != target.ID || candidate.Artifact != target.Artifact {
		t.Fatalf("candidate=%+v target=%+v", candidate, target)
	}
	if candidate.State != runtimev1.ReleaseCommittedToGitOps {
		t.Fatalf("candidate state=%s", candidate.State)
	}
	if _, ok := snapshot.Commits[candidate.ID]; !ok {
		t.Fatal("rollback release has no GitOps commit")
	}
}

func TestGitOpsCommit_ConflictingRecoveryIsRejected(t *testing.T) {
	f := newFixture(t)
	_, err := f.service.Deploy(context.Background(), f.request("conflict"))
	if err != nil {
		t.Fatal(err)
	}
	snapshot := f.store.Snapshot()
	var releaseID string
	for id := range snapshot.Releases {
		releaseID = id
	}
	err = f.store.Transact(context.Background(), func(tx application.Tx) error {
		record, ok := tx.GetGitOpsCommitByRelease(releaseID)
		if !ok {
			return errors.New("missing commit")
		}
		record.CommitSHA = "ffffffffffffffffffffffffffffffffffffffff"
		return tx.InsertGitOpsCommit(record)
	})
	if !domain.HasCode(err, domain.CodeConflict) {
		t.Fatalf("err=%v", err)
	}
}
