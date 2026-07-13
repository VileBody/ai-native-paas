package domain_test

import (
	"reflect"
	"testing"
	"time"

	"github.com/keir-research/ai-native-paas/internal/build/domain"
	buildv1 "github.com/keir-research/ai-native-paas/pkg/contracts/build/v1"
)

func newBuild(t *testing.T) domain.Build {
	t.Helper()
	b, err := domain.NewBuild("bld-1", "t1", "bldid-1", "cor-1", revision(repeat("a", 40)), domain.BuildConfig{}, digest("b"), digest("c"), "v1", time.Unix(1, 0))
	if err != nil {
		t.Fatal(err)
	}
	return b
}
func TestBuild_StartsQueued(t *testing.T) {
	if b := newBuild(t); b.State != buildv1.BuildQueued {
		t.Fatal(b.State)
	}
}
func TestBuild_AllowedTransitionMatrix(t *testing.T) {
	b := newBuild(t)
	now := time.Unix(2, 0)
	for _, state := range []buildv1.BuildState{buildv1.BuildFetchingSource, buildv1.BuildDetecting, buildv1.BuildBuilding, buildv1.BuildExporting, buildv1.BuildScanning, buildv1.BuildSigning} {
		if err := b.Transition(state, now); err != nil {
			t.Fatalf("%s: %v", state, err)
		}
	}
	if err := b.Succeed("art-1", now); err != nil {
		t.Fatal(err)
	}
}
func TestBuild_CannotSkipRequiredStage(t *testing.T) {
	b := newBuild(t)
	if err := b.Transition(buildv1.BuildBuilding, time.Now()); !domain.HasCode(err, domain.CodeConflict) {
		t.Fatalf("err=%v", err)
	}
}
func TestBuild_TerminalStateIsImmutable(t *testing.T) {
	b := newBuild(t)
	_ = b.Transition(buildv1.BuildFetchingSource, time.Now())
	_ = b.Fail(buildv1.BuildFailedUserCode, "SYNTAX", "bad", false, time.Now())
	if err := b.Transition(buildv1.BuildFetchingSource, time.Now()); !domain.HasCode(err, domain.CodeConflict) {
		t.Fatalf("err=%v", err)
	}
}
func TestBuild_CancelIsIdempotent(t *testing.T) {
	b := newBuild(t)
	if err := b.Cancel(time.Now()); err != nil {
		t.Fatal(err)
	}
	v := b.Version
	if err := b.Cancel(time.Now()); err != nil || b.Version != v {
		t.Fatalf("v=%d err=%v", b.Version, err)
	}
}
func TestBuild_NewerCommitCanSupersedeQueuedBuild(t *testing.T) {
	b := newBuild(t)
	if err := b.Supersede(time.Now(), false); err != nil || b.State != buildv1.BuildSuperseded {
		t.Fatal(b.State, err)
	}
}
func TestBuild_RunningBuildIsNotSupersededWithoutPolicy(t *testing.T) {
	b := newBuild(t)
	_ = b.Transition(buildv1.BuildFetchingSource, time.Now())
	if err := b.Supersede(time.Now(), false); !domain.HasCode(err, domain.CodeConflict) {
		t.Fatalf("err=%v", err)
	}
}
func TestBuild_RetryPreservesOriginalCorrelationID(t *testing.T) {
	b := newBuild(t)
	_ = b.Transition(buildv1.BuildFetchingSource, time.Now())
	_ = b.Fail(buildv1.BuildFailedPlatform, "CRASH", "bad", true, time.Now())
	r, err := b.Retry("bld-2", "cor-retry", time.Now())
	if err != nil || r.OriginalCorrelationID != "cor-1" || r.CorrelationID != "cor-retry" || r.Attempt != 2 {
		t.Fatalf("r=%+v err=%v", r, err)
	}
}
func TestBuild_TimeoutIsStableTerminalFailure(t *testing.T) {
	b := newBuild(t)
	_ = b.Transition(buildv1.BuildFetchingSource, time.Now())
	_ = b.Fail(buildv1.BuildTimedOut, "TIMEOUT", "too long", false, time.Now())
	if b.State != buildv1.BuildTimedOut || !b.Terminal() {
		t.Fatal(b.State)
	}
}

func TestBuild_SelectExecutionPersistsBackendAndTransitions(t *testing.T) {
	b := newBuild(t)
	now := time.Unix(2, 0).UTC()
	if err := b.Transition(buildv1.BuildFetchingSource, now); err != nil {
		t.Fatal(err)
	}
	if err := b.Transition(buildv1.BuildDetecting, now); err != nil {
		t.Fatal(err)
	}
	before := b.Version
	if err := b.SelectExecution("Go", domain.ExecutionBuildpacks, "paketo/go", now); err != nil {
		t.Fatal(err)
	}
	if b.State != buildv1.BuildBuilding || b.Runtime != "go" || b.Backend != domain.ExecutionBuildpacks || b.BuildpackID != "paketo/go" {
		t.Fatalf("build=%+v", b)
	}
	if b.Version != before+1 {
		t.Fatalf("version=%d before=%d", b.Version, before)
	}
}

func TestBuild_SelectExecutionRejectsInvalidOrRepeatedSelection(t *testing.T) {
	b := newBuild(t)
	now := time.Unix(2, 0).UTC()
	_ = b.Transition(buildv1.BuildFetchingSource, now)
	_ = b.Transition(buildv1.BuildDetecting, now)
	original := b
	if err := b.SelectExecution("go", domain.ExecutionBuildpacks, "", now); !domain.HasCode(err, domain.CodeInvalidArgument) {
		t.Fatalf("err=%v", err)
	}
	if !reflect.DeepEqual(b, original) {
		t.Fatalf("rejected selection mutated build: before=%+v after=%+v", original, b)
	}
	if err := b.SelectExecution("dockerfile", domain.ExecutionDockerfile, "unexpected", now); !domain.HasCode(err, domain.CodeInvalidArgument) {
		t.Fatalf("err=%v", err)
	}
	if err := b.SelectExecution("dockerfile", domain.ExecutionDockerfile, "", now); err != nil {
		t.Fatal(err)
	}
	if err := b.SelectExecution("go", domain.ExecutionBuildpacks, "paketo/go", now); !domain.HasCode(err, domain.CodeConflict) {
		t.Fatalf("repeated selection err=%v", err)
	}
}
