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

// ExitCodeFor returns the exit code for a given error code.
func ExitCodeFor(code string) int { return clioutput.ExitCodeFor(code) }
