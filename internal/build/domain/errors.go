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
	CodeUserFailure     ErrorCode = "USER_FAILURE"
	CodePlatformFailure ErrorCode = "PLATFORM_FAILURE"
	CodeTimeout         ErrorCode = "TIMEOUT"
	CodePolicyRejected  ErrorCode = "POLICY_REJECTED"
	CodeUnavailable     ErrorCode = "UNAVAILABLE"
)

type Error struct {
	Code      ErrorCode
	Message   string
	Retryable bool
	Cause     error
}

func (e *Error) Error() string {
	if e.Cause == nil {
		return fmt.Sprintf("%s: %s", e.Code, e.Message)
	}
	return fmt.Sprintf("%s: %s: %v", e.Code, e.Message, e.Cause)
}
func (e *Error) Unwrap() error { return e.Cause }

func NewError(code ErrorCode, message string) error {
	return &Error{Code: code, Message: message}
}
func Retryable(code ErrorCode, message string, cause error) error {
	return &Error{Code: code, Message: message, Retryable: true, Cause: cause}
}
func Wrap(code ErrorCode, message string, cause error) error {
	return &Error{Code: code, Message: message, Cause: cause}
}
func HasCode(err error, code ErrorCode) bool {
	var target *Error
	return errors.As(err, &target) && target.Code == code
}
func IsRetryable(err error) bool {
	var target *Error
	return errors.As(err, &target) && target.Retryable
}
