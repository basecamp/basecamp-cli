package commands

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The skill is published on merge, before any server activation. Its actual
// stop fragment must match runtime output, not a second frozen copy of it.
func TestTheSkillDocumentsTheDisconnectLineItGets(t *testing.T) {
	skill, err := os.ReadFile(filepath.Join("..", "..", "skills", "basecamp-connect", "SKILL.md"))
	require.NoError(t, err)
	fragment := regexp.MustCompile(`- \*\*"([^"]*disconnected[^"]*)"\*\*: run`).FindSubmatch(skill)
	require.Len(t, fragment, 2)
	message := errAgentDisconnected("Synthetic Agent", "laptop").Message
	assert.Contains(t, message, string(fragment[1]))
	assert.Contains(t, message, `Profile "laptop"`)
	assert.Contains(t, message, "or replaced")
}

func TestConnectionHelpIsSafeBeforeServerAddsForEitherKindOfAgent(t *testing.T) {
	help := newAuthAgentConnectCmd().Long
	assert.Contains(t, help, "approval may add a connection or replace")
	assert.Contains(t, help, "An agent, personal or team, keeps several")
	assert.Contains(t, help, "where Basecamp has enabled adding them for its kind")
	assert.NotContains(t, help, "always for shared")
	assert.NotContains(t, help, "Each approval adds")
	assert.Contains(t, help, "old connection stays live")
	assert.Contains(t, help, "not removed or revoked")
	help = NewConnectCmd().Long
	assert.Contains(t, help, "where Basecamp enables adding them for its kind")
	assert.NotContains(t, help, "shared agents")
	assert.Contains(t, help, "own hold")
}
