package domain

import (
	"errors"
	"fmt"
)

type ErrorCode string

const (
	CodeInvalidArgument ErrorCode = "INVALID_ARGUMENT"
	CodeNotFound        ErrorCode = "NOT_FOUND"
	CodeForbidden       ErrorCode = "FORBIDDEN"
	CodeConflict        ErrorCode = "CONFLICT"
	CodeStaleVersion    ErrorCode = "STALE_VERSION"
	CodePolicyRejected  ErrorCode = "POLICY_REJECTED"
	CodeCapacity        ErrorCode = "CAPACITY_EXHAUSTED"
	CodeUnavailable     ErrorCode = "UNAVAILABLE"
	CodeInternal        ErrorCode = "INTERNAL"
)

type Error struct {
	Code    ErrorCode
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
func NewError(code ErrorCode, message string) error { return &Error{Code: code, Message: message} }
func Wrap(code ErrorCode, message string, cause error) error {
	return &Error{Code: code, Message: message, Cause: cause}
}
func HasCode(err error, code ErrorCode) bool {
	var target *Error
	return errors.As(err, &target) && target.Code == code
}
