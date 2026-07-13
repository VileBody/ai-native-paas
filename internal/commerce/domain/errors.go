package domain

import (
	"errors"
	"fmt"
)

type Code string

const (
	CodeInvalidArgument  Code = "INVALID_ARGUMENT"
	CodeNotFound         Code = "NOT_FOUND"
	CodePermissionDenied Code = "PERMISSION_DENIED"
	CodeConflict         Code = "CONFLICT"
	CodeStaleVersion     Code = "STALE_VERSION"
	CodeQuotaExceeded    Code = "QUOTA_EXCEEDED"
	CodeUnavailable      Code = "UNAVAILABLE"
	CodeOverflow         Code = "OVERFLOW"
	CodeInternal         Code = "INTERNAL"
)

type Error struct {
	Code    Code
	Message string
	Cause   error
}

func (e *Error) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.Cause == nil {
		return fmt.Sprintf("%s: %s", e.Code, e.Message)
	}
	return fmt.Sprintf("%s: %s: %v", e.Code, e.Message, e.Cause)
}
func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}
func NewError(code Code, message string) error { return &Error{Code: code, Message: message} }
func Wrap(code Code, message string, cause error) error {
	return &Error{Code: code, Message: message, Cause: cause}
}
func HasCode(err error, code Code) bool {
	var target *Error
	return errors.As(err, &target) && target.Code == code
}
