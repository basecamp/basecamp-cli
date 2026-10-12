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

	previous := -1
	for _, step := range []string{
		"### 1. Start it, and watch its output",
		"### 2. The request line",
		"## For each request: acknowledge, choose the repo, hand off",
		"### a. Acknowledge, within seconds",
		"### b. Choose the repo",
		"### c. Dispatch one background subagent",
	} {
		at := strings.Index(body, step)
		assert.Greater(t, at, previous, "%q comes after the step before it", step)
		previous = at
	}
	assert.Contains(t, body[:dispatch], "~/.config/basecamp-connect/last.json",
		"choosing the repo names the file of confirmed mappings it reads")
	assert.Contains(t, body[:dispatch], "read `${CLAUDE_SKILL_DIR}/SKILL.md` in full",
		"a connector change the owner asks for mid-run reads the setup steps past the cut first")
	assert.Contains(t, body[:dispatch], "goes out as the agent,\nwith `--profile '<profile>'`",
		"the orchestrator's own replies say to post as the agent")
	assert.Contains(t, body[:dispatch], "after a change it restarts",
		"a setup change the owner asks for mid-run says the connector restarts to take it")
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
	assert.Contains(t, handoff, "`${CLAUDE_SKILL_DIR}/subagent.md`", "the handoff passes the file's path, which Claude Code expands")
	assert.Contains(t, handoff, "read it in full before anything else", "the handoff tells the subagent to read it first")

	// An acknowledgement such as "I'm on it" breaks a single-quoted command
	// unless its quote is escaped, so both files say how.
	acknowledge := skill[strings.Index(skill, "### a. Acknowledge, within seconds"):dispatch]
	assert.Contains(t, acknowledge, "`'\\''`", "the acknowledgement step says how to quote a value")
	assert.Contains(t, subagent, "`'\\''`", "the subagent's instructions say how to quote a value")
	for _, step := range []string{"**Shell quoting.**", "**1. Acknowledge", "**5. Reply at `reply_to`", "**6. Report back", "**A participant asks, an operator authorizes.**"} {
		assert.Contains(t, subagent, step)
	}
}
