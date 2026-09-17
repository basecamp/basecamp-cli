package commands

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// Copilot: Claude Code hands its MCP servers its own whole environment, so
// what the connector declared is a floor, not a ceiling. The bridge execs
// `basecamp mcp` with the declared environment alone, which is where the
// agent's own credentials stop.
func TestTheBridgeHandsOnOnlyTheEnvironmentTheConnectorDeclared(t *testing.T) {
	t.Setenv("HOME", "/home/agent")
	t.Setenv("BASECAMP_NO_KEYRING", "1")
	t.Setenv("ANTHROPIC_API_KEY", "test-key-not-real")
	t.Setenv("CLAUDE_CODE_MESSAGING_TOKEN", "test-token-not-real")
	t.Setenv("BASECAMP_CONNECT_TASK_TOKEN", "test-token-not-real")

	env := strings.Join(workerMCPEnv(), "\n")
	assert.NotContains(t, env, "ANTHROPIC_API_KEY", "the agent's own credential stops at the bridge")
	assert.NotContains(t, env, "CLAUDE_CODE_MESSAGING_TOKEN")
	assert.NotContains(t, env, "BASECAMP_CONNECT_TASK_TOKEN", "the token travels on a descriptor, not in an environment")
	assert.Contains(t, env, "HOME=/home/agent", "what the connector declared is kept")
	assert.Contains(t, env, "BASECAMP_NO_KEYRING=1")
}
