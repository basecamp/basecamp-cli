// Package errors provides SDK error types without CLI-specific hints.
// The CLI layer wraps these with user-facing hints.
package errors

import (
	"fmt"
)

// Error is a structured error for SDK operations.
// Unlike output.Error, it does not contain CLI-specific hints.
type Error struct {
	Code       string // Error code (e.g., "not_found", "auth_required")
	Message    string // Error message
	HTTPStatus int    // HTTP status code if applicable
	Retryable  bool   // Whether the operation can be retried
	Cause      error  // Underlying error
}

func (e *Error) Error() string {
	return e.Message
}

func (e *Error) Unwrap() error {
	return e.Cause
}

// Error codes.
const (
	CodeUsage     = "usage"
	CodeNotFound  = "not_found"
	CodeAuth      = "auth_required"
	CodeForbidden = "forbidden"
	CodeRateLimit = "rate_limit"
	CodeNetwork   = "network"
	CodeAPI       = "api_error"
	CodeAmbiguous = "ambiguous"
)

// Exit codes.
const (
	ExitOK        = 0 // Success
	ExitUsage     = 1 // Invalid arguments or flags
	ExitNotFound  = 2 // Resource not found
	ExitAuth      = 3 // Not authenticated
	ExitForbidden = 4 // Access denied (scope issue)
	ExitRateLimit = 5 // Rate limited (429)
	ExitNetwork   = 6 // Connection/DNS/timeout error
	ExitAPI       = 7 // Server returned error
	ExitAmbiguous = 8 // Multiple matches for name
)

// ExitCodeFor returns the exit code for a given error code.
func ExitCodeFor(code string) int {
	switch code {
	case CodeUsage:
		return ExitUsage
	case CodeNotFound:
		return ExitNotFound
	case CodeAuth:
		return ExitAuth
	case CodeForbidden:
		return ExitForbidden
	case CodeRateLimit:
		return ExitRateLimit
	case CodeNetwork:
		return ExitNetwork
	case CodeAPI:
		return ExitAPI
	case CodeAmbiguous:
		return ExitAmbiguous
	default:
		return ExitAPI
	}
}

// Error constructors.

// ErrUsage creates a usage error.
func ErrUsage(msg string) *Error {
	return &Error{Code: CodeUsage, Message: msg}
}

// ErrNotFound creates a not found error.
func ErrNotFound(resource, identifier string) *Error {
	return &Error{
		Code:       CodeNotFound,
		Message:    fmt.Sprintf("%s not found: %s", resource, identifier),
		HTTPStatus: 404,
	}
}

// ErrAuth creates an authentication error.
func ErrAuth(msg string) *Error {
	return &Error{
		Code:       CodeAuth,
		Message:    msg,
		HTTPStatus: 401,
	}
}

// ErrForbidden creates a forbidden error.
func ErrForbidden(msg string) *Error {
	return &Error{
		Code:       CodeForbidden,
		Message:    msg,
		HTTPStatus: 403,
	}
}

// ErrRateLimit creates a rate limit error.
func ErrRateLimit(retryAfter int) *Error {
	msg := "Rate limited"
	if retryAfter > 0 {
		msg = fmt.Sprintf("Rate limited (retry after %d seconds)", retryAfter)
	}
	return &Error{
		Code:       CodeRateLimit,
		Message:    msg,
		HTTPStatus: 429,
		Retryable:  true,
	}
}

// ErrNetwork creates a network error.
func ErrNetwork(cause error) *Error {
	return &Error{
		Code:      CodeNetwork,
		Message:   "Network error",
		Retryable: true,
		Cause:     cause,
	}
}

// ErrAPI creates an API error.
func ErrAPI(status int, msg string) *Error {
	return &Error{
		Code:       CodeAPI,
		Message:    msg,
		HTTPStatus: status,
	}
}

// ErrAmbiguous creates an ambiguous match error.
func ErrAmbiguous(resource string, matches []string) *Error {
	msg := fmt.Sprintf("Ambiguous %s", resource)
	if len(matches) > 0 && len(matches) <= 5 {
		msg = fmt.Sprintf("Ambiguous %s (matches: %v)", resource, matches)
	}
	return &Error{
		Code:    CodeAmbiguous,
		Message: msg,
	}
}
