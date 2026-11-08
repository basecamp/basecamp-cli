package harness

import (
	"os"
	"strings"
)

// InAgentSession reports whether this process runs inside a coding agent's
// session: Claude Code (CLAUDECODE, CLAUDE_CODE_SESSION_ID) or Codex (any
// CODEX_* variable but CODEX_HOME, which people set in their own shells to
// relocate Codex's files). It reads the environment only, so a process the
// agent starts, such as a script that posts, is inside the session too.
func InAgentSession() bool {
	return inAgentSession(os.Environ())
}

func inAgentSession(environ []string) bool {
	for _, kv := range environ {
		name, value, _ := strings.Cut(kv, "=")
		if value == "" {
			continue
		}
		switch {
		case name == "CLAUDECODE", name == "CLAUDE_CODE_SESSION_ID":
			return true
		case strings.HasPrefix(name, "CODEX_") && name != "CODEX_HOME":
			return true
		}
	}
	return false
}
