package kernel

import (
	"encoding/json"
	"testing"
	"time"

	kernelv1 "github.com/keir-research/ai-native-paas/contracts/kernel/v1"
)

func TestOperation_StartsPending(t *testing.T) {
	operation, err := NewOperation("op_1", "org_1", "test", "cor_1", "", testNow)
	if err != nil {
		t.Fatal(err)
	}
	if operation.State != kernelv1.OperationPending || operation.Version != 1 {
		t.Fatalf("operation = state %s version %d", operation.State, operation.Version)
	}
}

func TestOperation_AllowedTransitionMatrix(t *testing.T) {
	states := []kernelv1.OperationState{
		kernelv1.OperationPending,
		kernelv1.OperationRunning,
		kernelv1.OperationWaitingExternal,
		kernelv1.OperationSucceeded,
		kernelv1.OperationFailed,
		kernelv1.OperationCanceled,
	}
	expected := map[[2]kernelv1.OperationState]bool{
		{kernelv1.OperationPending, kernelv1.OperationRunning}:           true,
		{kernelv1.OperationPending, kernelv1.OperationFailed}:            true,
		{kernelv1.OperationPending, kernelv1.OperationCanceled}:          true,
		{kernelv1.OperationRunning, kernelv1.OperationWaitingExternal}:   true,
		{kernelv1.OperationRunning, kernelv1.OperationSucceeded}:         true,
		{kernelv1.OperationRunning, kernelv1.OperationFailed}:            true,
		{kernelv1.OperationRunning, kernelv1.OperationCanceled}:          true,
		{kernelv1.OperationWaitingExternal, kernelv1.OperationRunning}:   true,
		{kernelv1.OperationWaitingExternal, kernelv1.OperationSucceeded}: true,
		{kernelv1.OperationWaitingExternal, kernelv1.OperationFailed}:    true,
		{kernelv1.OperationWaitingExternal, kernelv1.OperationCanceled}:  true,
	}
	for _, from := range states {
		for _, to := range states {
			if got, want := AllowedOperationTransition(from, to), expected[[2]kernelv1.OperationState{from, to}]; got != want {
				t.Errorf("AllowedOperationTransition(%s, %s) = %v, want %v", from, to, got, want)
			}
		}
	}
}

func TestOperation_RejectsBackwardTransition(t *testing.T) {
	operation, _ := NewOperation("op_1", "org_1", "test", "cor_1", "", testNow)
	_ = operation.Transition(kernelv1.OperationRunning, kernelv1.OperationResult{}, testNow)
	_ = operation.Transition(kernelv1.OperationWaitingExternal, kernelv1.OperationResult{}, testNow)
	err := operation.Transition(kernelv1.OperationPending, kernelv1.OperationResult{}, testNow)
	if ErrorCode(err) != kernelv1.CodeInvalidTransition {
		t.Fatalf("transition error = %v, want INVALID_OPERATION_TRANSITION", err)
	}
}

func TestOperation_TerminalStateIsImmutable(t *testing.T) {
	terminal := []kernelv1.OperationState{kernelv1.OperationSucceeded, kernelv1.OperationFailed, kernelv1.OperationCanceled}
	for _, state := range terminal {
		t.Run(string(state), func(t *testing.T) {
			operation, _ := NewOperation("op_1", "org_1", "test", "cor_1", "", testNow)
			result := kernelv1.OperationResult{}
			if state == kernelv1.OperationSucceeded {
				if err := operation.Transition(kernelv1.OperationRunning, kernelv1.OperationResult{}, testNow); err != nil {
					t.Fatal(err)
				}
			}
			if state == kernelv1.OperationFailed {
				result.Error = &kernelv1.PublicError{Code: "FAILED_TEST", Message: "failed"}
			}
			if err := operation.Transition(state, result, testNow); err != nil {
				t.Fatal(err)
			}
			before := operation.Clone()
			err := operation.Transition(kernelv1.OperationRunning, kernelv1.OperationResult{}, testNow.Add(time.Second))
			if ErrorCode(err) != kernelv1.CodeInvalidTransition {
				t.Fatalf("transition error = %v", err)
			}
			if operation.State != before.State || operation.Version != before.Version || !operation.UpdatedAt.Equal(before.UpdatedAt) {
				t.Fatal("terminal operation mutated")
			}
		})
	}
}

func TestOperation_CancelIsIdempotent(t *testing.T) {
	operation, _ := NewOperation("op_1", "org_1", "test", "cor_1", "", testNow)
	if err := operation.Cancel(testNow); err != nil {
		t.Fatal(err)
	}
	version := operation.Version
	if err := operation.Cancel(testNow.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if operation.Version != version {
		t.Fatalf("version changed on idempotent cancel: %d -> %d", version, operation.Version)
	}
}

func TestOperation_RecordsStableErrorCode(t *testing.T) {
	operation, _ := NewOperation("op_1", "org_1", "test", "cor_1", "", testNow)
	err := operation.Transition(kernelv1.OperationFailed, kernelv1.OperationResult{Error: &kernelv1.PublicError{Message: "missing code"}}, testNow)
	if ErrorCode(err) != kernelv1.CodeInvalidArgument {
		t.Fatalf("transition error = %v, want INVALID_ARGUMENT", err)
	}
	stable := &kernelv1.PublicError{Code: "BUILD_FAILED", Message: "build failed", Retryable: false}
	if err := operation.Transition(kernelv1.OperationFailed, kernelv1.OperationResult{Error: stable}, testNow); err != nil {
		t.Fatal(err)
	}
	snapshot := operation.Snapshot()
	encoded, _ := json.Marshal(snapshot)
	if snapshot.Result.Error == nil || snapshot.Result.Error.Code != "BUILD_FAILED" || len(encoded) == 0 {
		t.Fatalf("stable error not retained: %+v", snapshot.Result.Error)
	}
}

func FuzzOperationTransitionNeverMutatesOnRejectedTransition(f *testing.F) {
	f.Add(uint8(0), uint8(1))
	f.Add(uint8(3), uint8(1))
	states := []kernelv1.OperationState{kernelv1.OperationPending, kernelv1.OperationRunning, kernelv1.OperationWaitingExternal, kernelv1.OperationSucceeded, kernelv1.OperationFailed, kernelv1.OperationCanceled}
	f.Fuzz(func(t *testing.T, fromIndex, toIndex uint8) {
		from := states[int(fromIndex)%len(states)]
		to := states[int(toIndex)%len(states)]
		operation, _ := NewOperation("op_1", "org_1", "test", "cor_1", "", testNow)
		operation.State = from
		before := operation.Clone()
		result := kernelv1.OperationResult{}
		if to == kernelv1.OperationFailed {
			result.Error = &kernelv1.PublicError{Code: "TEST_FAILURE", Message: "failure"}
		}
		err := operation.Transition(to, result, testNow.Add(time.Minute))
		if err != nil && (operation.State != before.State || operation.Version != before.Version || !operation.UpdatedAt.Equal(before.UpdatedAt)) {
			t.Fatalf("rejected transition %s -> %s mutated operation", from, to)
		}
	})
}
