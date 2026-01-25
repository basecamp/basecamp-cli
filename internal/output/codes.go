// Package output provides JSON/Markdown output formatting and error handling.
package output

// Exit codes matching the Bash implementation.
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

// Error codes for JSON envelope.
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
