package connector

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// perRequestBudget bounds where the orchestrator's per-request steps may end
// in the skill's body. After a compaction Claude Code re-attaches a skill cut
// to its first 5,000 tokens (code.claude.com/docs/en/skills, "Skill content
// lifecycle"); this skill's Markdown runs near 2.9 characters a token, so
// 14,000 characters stays under the cut with room to spare. A session cut
// short of the handoff can acknowledge a request and never dispatch it.
const perRequestBudget = 14000

func readConnectSkill(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "skills", "basecamp-connect", name))
	require.NoError(t, err)
	return strings.ReplaceAll(string(raw), "\r\n", "\n")
}

// skillBody drops the frontmatter, which Claude Code doesn't render into the
// conversation.
func skillBody(t *testing.T, skill string) string {
	t.Helper()
	parts := strings.SplitN(skill, "---\n", 3)
	require.Len(t, parts, 3, "the skill opens with frontmatter")
	return parts[2]
}

func TestTheConnectSkillKeepsEachRequestsStepsInsideTheCompactionCut(t *testing.T) {
	body := skillBody(t, readConnectSkill(t, "SKILL.md"))
	dispatch := strings.Index(body, "### c. Dispatch one background subagent")
	require.GreaterOrEqual(t, dispatch, 0, "the handoff step is where this test looks for it")
	end := strings.Index(body[dispatch:], "\n## ")
	require.GreaterOrEqual(t, end, 0, "a section follows the handoff")
	end += dispatch

	for _, step := range []string{
		"### 1. Start it, and watch its output",
		"### 2. The request line",
		"## For each request: acknowledge, choose the repo, hand off",
		"### a. Acknowledge, within seconds",
		"### b. Choose the repo",
	} {
		at := strings.Index(body, step)
		assert.True(t, at >= 0 && at < dispatch, "%q comes before the handoff", step)
	}
	assert.LessOrEqual(t, end, perRequestBudget,
		"the per-request steps end at character %d of the skill's body; keep them within %d, moving setup and reference material after them",
		end, perRequestBudget)
}

func TestTheSubagentReadsItsInstructionsFromTheirOwnFile(t *testing.T) {
	skill := readConnectSkill(t, "SKILL.md")
	subagent := readConnectSkill(t, "subagent.md")

	assert.NotContains(t, skill, "## The subagent's instructions", "the subagent's instructions live in subagent.md")
	dispatch := strings.Index(skill, "### c. Dispatch one background subagent")
	require.GreaterOrEqual(t, dispatch, 0)
	handoff := skill[dispatch:]
	handoff = handoff[:strings.Index(handoff, "\n## ")]
	assert.Contains(t, handoff, "the path of `subagent.md`", "the handoff passes the file the subagent reads")
	assert.Contains(t, handoff, "read it in full before anything else", "the handoff tells the subagent to read it first")
	for _, step := range []string{"**Shell quoting.**", "**1. Acknowledge", "**5. Reply at `reply_to`", "**6. Report back", "**A participant asks, an operator authorizes.**"} {
		assert.Contains(t, subagent, step)
	}
}
