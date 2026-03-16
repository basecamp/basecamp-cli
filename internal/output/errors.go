package output

import (
	"errors"
	"fmt"
	"strings"

	clioutput "github.com/basecamp/cli/output"
)

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

// errJQUnsupported is a sentinel cause for all jq-related errors.
// Root.go uses IsJQError() to detect these and bypass jq filtering
// when rendering the error itself.
var errJQUnsupported = errors.New("jq unsupported")

// ErrJQValidation returns a usage error for invalid --jq expressions.
func ErrJQValidation(cause error) *Error {
	return &Error{
		Code:    CodeUsage,
		Message: fmt.Sprintf("invalid --jq expression: %s", cause),
		Cause:   errJQUnsupported,
	}
}

// ErrJQNotSupported returns a usage error for commands that don't support --jq.
func ErrJQNotSupported(command string) *Error {
	return &Error{
		Code:    CodeUsage,
		Message: fmt.Sprintf("--jq is not supported by %s", command),
		Cause:   errJQUnsupported,
	}
}

// ErrJQConflict returns a usage error for flags that conflict with --jq.
func ErrJQConflict(flag string) *Error {
	return &Error{
		Code:    CodeUsage,
		Message: fmt.Sprintf("cannot use --jq with %s", flag),
		Cause:   errJQUnsupported,
	}
}

// ErrJQRuntime returns a usage error for jq runtime failures
// (e.g. type errors, non-serializable results).
func ErrJQRuntime(cause error) *Error {
	return &Error{
		Code:    CodeUsage,
		Message: fmt.Sprintf("jq filter error: %s", cause),
		Cause:   errJQUnsupported,
	}
}

// IsJQError returns true if the error is a jq-related error
// (validation failure, unsupported command, or flag conflict).
func IsJQError(err error) bool {
	return errors.Is(err, errJQUnsupported)
}

// PluralNoun returns a simple English plural for tool-related nouns.
// Handles the sibilant cases we encounter (inbox → inboxes) and falls
// back to appending "s".
func PluralNoun(s string) string {
	if strings.HasSuffix(s, "x") || strings.HasSuffix(s, "sh") || strings.HasSuffix(s, "ch") || strings.HasSuffix(s, "ss") {
		return s + "es"
	}
	return s + "s"
}
