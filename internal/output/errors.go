package output

import (
	"errors"
	"fmt"
)

// Error is a structured error with code, message, and optional hint.
type Error struct {
	Code       string
	Message    string
	Hint       string
	HTTPStatus int
	Retryable  bool
	Cause      error
}

func (e *Error) Error() string {
	if e.Hint != "" {
		return fmt.Sprintf("%s: %s", e.Message, e.Hint)
	}
	return e.Message
}

func (e *Error) Unwrap() error {
	return e.Cause
}

// ExitCode returns the appropriate exit code for this error.
func (e *Error) ExitCode() int {
	return ExitCodeFor(e.Code)
}

// Error constructors for common cases.

func ErrUsage(msg string) *Error {
	return &Error{Code: CodeUsage, Message: msg}
}

func ErrUsageHint(msg, hint string) *Error {
	return &Error{Code: CodeUsage, Message: msg, Hint: hint}
}

func ErrNotFound(resource, identifier string) *Error {
	return &Error{
		Code:    CodeNotFound,
		Message: fmt.Sprintf("%s not found: %s", resource, identifier),
	}
}

func ErrNotFoundHint(resource, identifier, hint string) *Error {
	return &Error{
		Code:    CodeNotFound,
		Message: fmt.Sprintf("%s not found: %s", resource, identifier),
		Hint:    hint,
	}
}

func ErrAuth(msg string) *Error {
	return &Error{
		Code:    CodeAuth,
		Message: msg,
		Hint:    "Run: basecamp auth login",
	}
}

func ErrForbidden(msg string) *Error {
	return &Error{
		Code:       CodeForbidden,
		Message:    msg,
		HTTPStatus: 403,
	}
}

func ErrForbiddenScope() *Error {
	return &Error{
		Code:       CodeForbidden,
		Message:    "Access denied: insufficient scope",
		Hint:       "Run: basecamp auth login --scope full",
		HTTPStatus: 403,
	}
}

func ErrRateLimit(retryAfter int) *Error {
	hint := "Try again later"
	if retryAfter > 0 {
		hint = fmt.Sprintf("Try again in %d seconds", retryAfter)
	}
	return &Error{
		Code:       CodeRateLimit,
		Message:    "Rate limited",
		Hint:       hint,
		HTTPStatus: 429,
		Retryable:  true,
	}
}

func ErrNetwork(cause error) *Error {
	return &Error{
		Code:      CodeNetwork,
		Message:   "Network error",
		Hint:      cause.Error(),
		Retryable: true,
		Cause:     cause,
	}
}

func ErrAPI(status int, msg string) *Error {
	return &Error{
		Code:       CodeAPI,
		Message:    msg,
		HTTPStatus: status,
	}
}

func ErrAmbiguous(resource string, matches []string) *Error {
	hint := "Be more specific"
	if len(matches) > 0 && len(matches) <= 5 {
		hint = fmt.Sprintf("Did you mean: %v", matches)
	}
	return &Error{
		Code:    CodeAmbiguous,
		Message: fmt.Sprintf("Ambiguous %s", resource),
		Hint:    hint,
	}
}

// AsError attempts to convert an error to an *Error.
func AsError(err error) *Error {
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	return &Error{
		Code:    CodeAPI,
		Message: err.Error(),
		Cause:   err,
	}
}
