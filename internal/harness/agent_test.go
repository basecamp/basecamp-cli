package harness

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRegisterAndFindAgent(t *testing.T) {
	resetRegistry()
	defer resetRegistry()

	RegisterAgent(AgentInfo{
		Name:   "Test Agent",
		ID:     "test",
		Detect: func() bool { return true },
	})

	found := FindAgent("test")
	require.NotNil(t, found)
	assert.Equal(t, "Test Agent", found.Name)
	assert.Equal(t, "test", found.ID)
}

func TestFindAgent_NotFound(t *testing.T) {
	resetRegistry()
	defer resetRegistry()

	assert.Nil(t, FindAgent("nonexistent"))
}

func TestDetectedAgents(t *testing.T) {
	resetRegistry()
	defer resetRegistry()

	RegisterAgent(AgentInfo{ID: "yes", Detect: func() bool { return true }})
	RegisterAgent(AgentInfo{ID: "no", Detect: func() bool { return false }})
	RegisterAgent(AgentInfo{ID: "also-yes", Detect: func() bool { return true }})

	detected := DetectedAgents()
	assert.Len(t, detected, 2)
	assert.Equal(t, "yes", detected[0].ID)
	assert.Equal(t, "also-yes", detected[1].ID)
}

func TestAllAgents(t *testing.T) {
	resetRegistry()
	defer resetRegistry()

	RegisterAgent(AgentInfo{ID: "a"})
	RegisterAgent(AgentInfo{ID: "b"})

	all := AllAgents()
	assert.Len(t, all, 2)
}

func TestDetectedAgents_Empty(t *testing.T) {
	resetRegistry()
	defer resetRegistry()

	assert.Empty(t, DetectedAgents())
}

func TestRegisterAgent_EmptyID(t *testing.T) {
	resetRegistry()
	defer resetRegistry()

	assert.Panics(t, func() {
		RegisterAgent(AgentInfo{Name: "Bad Agent"})
	})
}

func TestRegisterAgent_DuplicateID(t *testing.T) {
	resetRegistry()
	defer resetRegistry()

	RegisterAgent(AgentInfo{ID: "dup", Name: "First"})
	assert.Panics(t, func() {
		RegisterAgent(AgentInfo{ID: "dup", Name: "Second"})
	})
}

func TestClaudeAgentInfoWiring(t *testing.T) {
	// Verifies the Claude AgentInfo registration contract: DetectClaude and
	// check functions are correctly wired up.
	resetRegistry()
	defer resetRegistry()

	RegisterAgent(AgentInfo{
		Name:   "Claude Code",
		ID:     "claude",
		Detect: DetectClaude,
		Checks: func() []*StatusCheck { return []*StatusCheck{CheckClaudePlugin()} },
	})

	found := FindAgent("claude")
	require.NotNil(t, found)
	assert.Equal(t, "Claude Code", found.Name)
	assert.NotNil(t, found.Detect)
	assert.NotNil(t, found.Checks)
}
