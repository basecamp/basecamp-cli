// Package output provides JSON/Markdown output formatting and error handling.
package output

import clioutput "github.com/basecamp/cli/output"

// Exit codes matching the Bash implementation (re-exported from shared module).
const (
	ExitOK        = clioutput.ExitOK
	ExitUsage     = clioutput.ExitUsage
	ExitNotFound  = clioutput.ExitNotFound
	ExitAuth      = clioutput.ExitAuth
	ExitForbidden = clioutput.ExitForbidden
	ExitRateLimit = clioutput.ExitRateLimit
	ExitNetwork   = clioutput.ExitNetwork
	ExitAPI       = clioutput.ExitAPI
	ExitAmbiguous = clioutput.ExitAmbiguous
)

// Error codes for JSON envelope (re-exported from shared module).
const (
	CodeUsage     = clioutput.CodeUsage
	CodeNotFound  = clioutput.CodeNotFound
	CodeAuth      = clioutput.CodeAuth
	CodeForbidden = clioutput.CodeForbidden
	CodeRateLimit = clioutput.CodeRateLimit
	CodeNetwork   = clioutput.CodeNetwork
	CodeAPI       = clioutput.CodeAPI
	CodeAmbiguous = clioutput.CodeAmbiguous
)

// Codes the shared module does not know yet. The SDK started emitting both at
// v0.13.0/v0.14.0 (422 validation, 507 account limits); values match the SDK's
// own exit-code mapping. Local arms below keep them from collapsing into the
// shared table's ExitAPI default until the shared module catches up.
const (
	CodeValidation    = "validation"
	CodeLimitExceeded = "limit_exceeded"

	ExitValidation = 9  // Validation error (422)
	ExitLimit      = 10 // Account limit reached (507)
)

// ExitCodeFor returns the exit code for a given error code.
func ExitCodeFor(code string) int {
	switch code {
	case CodeValidation:
		return ExitValidation
	case CodeLimitExceeded:
		return ExitLimit
	}
	return clioutput.ExitCodeFor(code)
}
