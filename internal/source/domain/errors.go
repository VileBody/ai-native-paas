package domain

import (
	"errors"
	"fmt"
)

type ErrorCode string

const (
	CodeInvalidArgument ErrorCode = "INVALID_ARGUMENT"
	CodeNotFound        ErrorCode = "NOT_FOUND"
	CodeConflict        ErrorCode = "CONFLICT"
	CodeForbidden       ErrorCode = "FORBIDDEN"
	CodeStaleVersion    ErrorCode = "STALE_VERSION"
	CodeExternal        ErrorCode = "EXTERNAL_FAILURE"
	CodeUnavailable     ErrorCode = "UNAVAILABLE"
)

type Error struct {
	Code    ErrorCode
	Message string
	Cause   error
}

func (e *Error) Error() string {
	if e.Cause == nil {
		return fmt.Sprintf("%s: %s", e.Code, e.Message)
	}
	return fmt.Sprintf("%s: %s: %v", e.Code, e.Message, e.Cause)
}
func (e *Error) Unwrap() error                      { return e.Cause }
func NewError(code ErrorCode, message string) error { return &Error{Code: code, Message: message} }
func Wrap(code ErrorCode, message string, cause error) error {
	return &Error{Code: code, Message: message, Cause: cause}
}
func HasCode(err error, code ErrorCode) bool {
	var e *Error
	return errors.As(err, &e) && e.Code == code
}
