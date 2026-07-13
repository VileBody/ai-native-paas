package kernel

import (
	"errors"
	"fmt"

	kernelv1 "github.com/keir-research/ai-native-paas/contracts/kernel/v1"
)

type DomainError struct {
	Code      string
	Message   string
	Retryable bool
	Details   map[string]any
	cause     error
}

func (e *DomainError) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}

func (e *DomainError) Unwrap() error { return e.cause }

func NewError(code, message string) *DomainError {
	return &DomainError{Code: code, Message: message}
}

func WrapError(code, message string, retryable bool, cause error) *DomainError {
	return &DomainError{Code: code, Message: message, Retryable: retryable, cause: cause}
}

func ErrorCode(err error) string {
	var domainErr *DomainError
	if errors.As(err, &domainErr) {
		return domainErr.Code
	}
	return kernelv1.CodeInternal
}

func ToPublicError(err error, operationID kernelv1.OperationID) kernelv1.PublicError {
	var domainErr *DomainError
	if errors.As(err, &domainErr) {
		details := cloneMap(domainErr.Details)
		return kernelv1.PublicError{
			Code:        domainErr.Code,
			Message:     domainErr.Message,
			OperationID: operationID,
			Retryable:   domainErr.Retryable,
			Details:     details,
		}
	}
	return kernelv1.PublicError{
		Code:        kernelv1.CodeInternal,
		Message:     "Internal platform error",
		OperationID: operationID,
		Retryable:   true,
	}
}

func invalidArgument(format string, args ...any) *DomainError {
	return NewError(kernelv1.CodeInvalidArgument, fmt.Sprintf(format, args...))
}

func notFound(resource string) *DomainError {
	return NewError(kernelv1.CodeNotFound, resource+" not found")
}

func conflict(message string) *DomainError {
	return NewError(kernelv1.CodeConflict, message)
}

func forbidden(message string) *DomainError {
	return NewError(kernelv1.CodeForbidden, message)
}

func optimisticLock() *DomainError {
	return &DomainError{Code: kernelv1.CodeOptimisticLock, Message: "Concurrent modification detected", Retryable: true}
}

func cloneMap(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
