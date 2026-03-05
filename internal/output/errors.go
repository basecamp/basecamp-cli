package output

import clioutput "github.com/basecamp/cli/output"

// Error is a structured error with code, message, and optional hint.
// Type alias — zero-cost, full compatibility with errors.As.
type Error = clioutput.Error

// Generic error constructors (re-exported from shared module).
var (
	ErrUsage        = clioutput.ErrUsage
	ErrUsageHint    = clioutput.ErrUsageHint
	ErrNotFound     = clioutput.ErrNotFound
	ErrNotFoundHint = clioutput.ErrNotFoundHint
	ErrForbidden    = clioutput.ErrForbidden
	ErrRateLimit    = clioutput.ErrRateLimit
	ErrNetwork      = clioutput.ErrNetwork
	ErrAPI          = clioutput.ErrAPI
	ErrAmbiguous    = clioutput.ErrAmbiguous
	AsError         = clioutput.AsError
)

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
