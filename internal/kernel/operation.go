package kernel

import (
	"encoding/json"
	"strings"
	"time"

	kernelv1 "github.com/keir-research/ai-native-paas/contracts/kernel/v1"
)

type Operation struct {
	ID            kernelv1.OperationID
	TenantID      kernelv1.TenantID
	Kind          string
	State         kernelv1.OperationState
	CorrelationID kernelv1.CorrelationID
	CausationID   kernelv1.CausationID
	Result        kernelv1.OperationResult
	Version       int64
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

func NewOperation(id kernelv1.OperationID, tenantID kernelv1.TenantID, kind string, correlationID kernelv1.CorrelationID, causationID kernelv1.CausationID, now time.Time) (*Operation, error) {
	if strings.TrimSpace(string(id)) == "" {
		return nil, invalidArgument("operation id is required")
	}
	if strings.TrimSpace(kind) == "" {
		return nil, invalidArgument("operation kind is required")
	}
	if strings.TrimSpace(string(correlationID)) == "" {
		return nil, invalidArgument("operation correlation id is required")
	}
	now = now.UTC()
	return &Operation{
		ID:            id,
		TenantID:      tenantID,
		Kind:          kind,
		State:         kernelv1.OperationPending,
		CorrelationID: correlationID,
		CausationID:   causationID,
		Version:       1,
		CreatedAt:     now,
		UpdatedAt:     now,
	}, nil
}

func (o *Operation) Clone() *Operation {
	if o == nil {
		return nil
	}
	copy := *o
	copy.Result.Data = append(json.RawMessage(nil), o.Result.Data...)
	if o.Result.Error != nil {
		publicErr := *o.Result.Error
		publicErr.Details = cloneMap(o.Result.Error.Details)
		copy.Result.Error = &publicErr
	}
	return &copy
}

func (o *Operation) Transition(to kernelv1.OperationState, result kernelv1.OperationResult, now time.Time) error {
	if o.State == to && to == kernelv1.OperationCanceled {
		return nil
	}
	if o.State.Terminal() {
		return NewError(kernelv1.CodeInvalidTransition, "terminal operation state is immutable")
	}
	if !AllowedOperationTransition(o.State, to) {
		return NewError(kernelv1.CodeInvalidTransition, "operation state transition is not allowed")
	}
	if to == kernelv1.OperationFailed {
		if result.Error == nil || strings.TrimSpace(result.Error.Code) == "" {
			return invalidArgument("failed operation requires a stable error code")
		}
	}
	if to == kernelv1.OperationSucceeded && result.Error != nil {
		return invalidArgument("successful operation cannot contain an error")
	}
	o.State = to
	o.Result = result
	o.Version++
	o.UpdatedAt = now.UTC()
	return nil
}

func (o *Operation) Cancel(now time.Time) error {
	if o.State == kernelv1.OperationCanceled {
		return nil
	}
	return o.Transition(kernelv1.OperationCanceled, kernelv1.OperationResult{Code: "CANCELED"}, now)
}

func AllowedOperationTransition(from, to kernelv1.OperationState) bool {
	switch from {
	case kernelv1.OperationPending:
		return to == kernelv1.OperationRunning || to == kernelv1.OperationFailed || to == kernelv1.OperationCanceled
	case kernelv1.OperationRunning:
		return to == kernelv1.OperationWaitingExternal || to == kernelv1.OperationSucceeded || to == kernelv1.OperationFailed || to == kernelv1.OperationCanceled
	case kernelv1.OperationWaitingExternal:
		return to == kernelv1.OperationRunning || to == kernelv1.OperationSucceeded || to == kernelv1.OperationFailed || to == kernelv1.OperationCanceled
	default:
		return false
	}
}

func (o *Operation) Snapshot() kernelv1.OperationSnapshot {
	clone := o.Clone()
	return kernelv1.OperationSnapshot{
		OperationRef: kernelv1.OperationRef{
			OperationID: clone.ID,
			TenantID:    clone.TenantID,
			State:       clone.State,
		},
		Kind:          clone.Kind,
		CorrelationID: clone.CorrelationID,
		CausationID:   clone.CausationID,
		Result:        clone.Result,
		Version:       clone.Version,
		CreatedAt:     clone.CreatedAt,
		UpdatedAt:     clone.UpdatedAt,
	}
}
