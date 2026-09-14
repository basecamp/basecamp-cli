package output

import (
	"errors"
	"fmt"
	"strings"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"
	clioutput "github.com/basecamp/cli/output"

	"github.com/basecamp/basecamp-cli/internal/resilience"
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

// AsError converts err to the CLI's structured error. An *Error already in
// the chain is the CLI's own verdict and wins over the SDK error it may
// wrap as its cause; only a bare SDK error is converted from its taxonomy.
func AsError(err error) *Error {
	if gateErr := AsGateError(err); gateErr != nil {
		return gateErr
	}
	var cliErr *Error
	if errors.As(err, &cliErr) {
		return WithAuthHint(cliErr)
	}
	var sdkErr *basecamp.Error
	if errors.As(err, &sdkErr) {
		message := err.Error()
		if sdkErr.Hint != "" {
			message = strings.TrimSuffix(message, ": "+sdkErr.Hint)
		}
		if message == "" {
			message = sdkErr.Message
		}
		return WithAuthHint(&Error{
			Code:       sdkErr.Code,
			Message:    message,
			Hint:       sdkErr.Hint,
			HTTPStatus: sdkErr.HTTPStatus,
			Retryable:  sdkErr.Retryable,
			Cause:      sdkErr,
		})
	}
	return WithAuthHint(clioutput.AsError(err))
}

// DefaultAuthHint is the remedy an auth_required error carries when nothing
// closer to the credential has named one. The app boundary (appctx.App.Err)
// replaces it with a remedy that knows the active profile and whether
// BASECAMP_TOKEN is in play; this is the floor beneath that.
const DefaultAuthHint = "Run: basecamp auth login"

// WithAuthHint adds the login remedy to an auth_required error that has
// none. A 401 from the API and the SDK's own auth errors arrive without a
// hint, and "authentication required" alone leaves the reader to guess what
// to run. Errors that already carry a hint, or are not auth errors, pass
// through untouched; a hinted copy is returned so the caller's error is not
// rewritten under it.
func WithAuthHint(e *Error) *Error {
	if e.Code != CodeAuth || e.Hint != "" {
		return e
	}
	hinted := *e
	hinted.Hint = DefaultAuthHint
	return &hinted
}

// AsGateError converts a resilience gate rejection, which arrives through
// any SDK operation, into the rate-limit error the user sees: the gate's own
// message and hint (which limit, how long it waited, what to do), retryable.
// Nil when err is not a gate rejection.
func AsGateError(err error) *Error {
	var gateErr *resilience.GateError
	if !errors.As(err, &gateErr) {
		return nil
	}
	return &Error{
		Code:      CodeRateLimit,
		Message:   gateErr.Message,
		Hint:      gateErr.Hint,
		Retryable: true,
	}
}

// RequestID returns the SDK request ID carried by err, if present.
func RequestID(err error) string {
	var sdkErr *basecamp.Error
	if errors.As(err, &sdkErr) {
		return sdkErr.RequestID
	}
	return ""
}

// App-specific error constructors with basecamp-cli hints.

func ErrAuth(msg string) *Error {
	return &Error{
		Code:    CodeAuth,
		Message: msg,
		Hint:    DefaultAuthHint,
	}
}

// ErrInterrupted reports a command stopped by the person at the terminal.
// msg is for machine consumers; the command prints its own human line.
func ErrInterrupted(msg string) *Error {
	return &Error{Code: CodeInterrupted, Message: msg}
}

// ErrTerminated reports a command stopped by a SIGTERM while it waited.
// msg is for machine consumers; the command prints its own human line.
func ErrTerminated(msg string) *Error {
	return &Error{Code: CodeTerminated, Message: msg}
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
