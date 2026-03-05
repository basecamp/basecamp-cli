package output

import clioutput "github.com/basecamp/cli/output"

// Error is a structured error with code, message, and optional hint.
// Type alias — zero-cost, full compatibility with errors.As.
type Error = clioutput.Error

// Generic error constructors (re-exported from shared module).

func ErrUsage(msg string) *Error           { return clioutput.ErrUsage(msg) }
func ErrUsageHint(msg, hint string) *Error { return clioutput.ErrUsageHint(msg, hint) }
func ErrNotFound(resource, identifier string) *Error {
	return clioutput.ErrNotFound(resource, identifier)
}
func ErrNotFoundHint(resource, identifier, hint string) *Error {
	return clioutput.ErrNotFoundHint(resource, identifier, hint)
}
func ErrForbidden(msg string) *Error       { return clioutput.ErrForbidden(msg) }
func ErrRateLimit(retryAfter int) *Error   { return clioutput.ErrRateLimit(retryAfter) }
func ErrNetwork(cause error) *Error        { return clioutput.ErrNetwork(cause) }
func ErrAPI(status int, msg string) *Error { return clioutput.ErrAPI(status, msg) }
func ErrAmbiguous(resource string, matches []string) *Error {
	return clioutput.ErrAmbiguous(resource, matches)
}
func AsError(err error) *Error { return clioutput.AsError(err) }

// App-specific error constructors with basecamp-cli hints.

func ErrAuth(msg string) *Error {
	return &Error{
		Code:    CodeAuth,
		Message: msg,
		Hint:    "Run: basecamp auth login",
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
