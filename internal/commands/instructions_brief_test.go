package commands

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// instructionsStyleFixture has the shape of a house style guide: a heading,
// prose, then a numbered list of rules, each led by a bold sentence and
// wrapped over several lines.
const instructionsStyleFixture = `# Writing in Basecamp

How every agent writes what it posts to Basecamp. Change it by pull request,
so the review is what lets agents trust it.

Write each post for the person who will read it, where they will read it.

1. **Speak only when it helps someone.** Post when asked, mentioned or
   assigned, or when someone needs a result, decision or question from you.
2. **Keep private context private.** A public post rests only on what readers
   there can see; if you know something only from private messages, stay
   silent in public unless your operator asks you to act there.
3. **Write for someone who hasn't followed the work.** Tell them plainly what
   they see, lose or can now do, with the mechanism after, if at all.
4. **Lead with the point.** The first sentence answers the question in its own
   terms, gives the bottom line, or says what you need.
5. **Use the reader's words.** Replace internal names, codes and file paths
   with plain words.
6. **Say exactly what is true, in plain sentences.** Use literal words that
   match what happened; no metaphors or flourishes.
7. **Make an ask unmistakable.** Ask the person whose call it is with a real
   mention: what you need, why them, and enough context to decide.
8. **Keep the work out of the post.** Give the conclusion; put the logs and
   steps behind it on the linked work, and never fold an ask or a blocker.
9. **Don't perform, and say each thing once.** No preamble, flattery,
   apologies or recap; if you were wrong, state the corrected fact once.
10. **Keep each post to one topic, and lay it out to be scanned.** Separate
    unrelated matters, put several items in a real list, and link every
    reference on the words that say what it is.
11. **Show what is visual, and check what readers see.** Use a screenshot,
    chart or small table when the point is visual or numeric, and look at the
    published post to fix what came out wrong.
`

func TestInstructionsBriefTakesTheBoldLead(t *testing.T) {
	brief := instructionsBrief(instructionsStyleFixture)
	require.Len(t, brief, 11)
	assert.Equal(t, "1. Speak only when it helps someone.", brief[0])
	assert.Equal(t, "3. Write for someone who hasn't followed the work.", brief[2])
	assert.Equal(t, "10. Keep each post to one topic, and lay it out to be scanned.", brief[9])
	assert.Equal(t, "11. Show what is visual, and check what readers see.", brief[10])
	words := len(strings.Fields(strings.Join(brief, " ")))
	assert.Less(t, words, 110, "about a hundred words for a guide of this shape")
}

func TestInstructionsBrief(t *testing.T) {
	cases := map[string]struct {
		content string
		want    []string
	}{
		"bold lead": {
			"- **Be brief.** Say it once.\n- **Link it.**\n",
			[]string{"- Be brief.", "- Link it."},
		},
		"underscore bold": {
			"1. __Be brief.__ Say it once.",
			[]string{"1. Be brief."},
		},
		"bold that wraps a line": {
			"1. **Keep each post to one\n   topic.** Separate matters.",
			[]string{"1. Keep each post to one topic."},
		},
		"no bold: first sentence": {
			"1. Lead with the point. Then the rest.\n2. Use plain words! No jargon.\n3. Ask? Yes.",
			[]string{"1. Lead with the point.", "2. Use plain words!", "3. Ask?"},
		},
		"first sentence wraps a line": {
			"- Lead with the point\n  in the first line. Then the rest.",
			[]string{"- Lead with the point in the first line."},
		},
		"no sentence end: the whole first paragraph": {
			"- Lead with the point\n\n  A second paragraph.",
			[]string{"- Lead with the point"},
		},
		"bullets of every kind": {
			"* One.\n+ Two.\n- Three.",
			[]string{"- One.", "- Two.", "- Three."},
		},
		"numbers with a parenthesis": {
			"1) One.\n2) Two.",
			[]string{"1) One.", "2) Two."},
		},
		"nested items ignored": {
			"1. **Top.** Body.\n   - Nested one.\n   - Nested two.\n2. Next.\n    1. Deeper.\n        - Deepest.",
			[]string{"1. Top.", "2. Next."},
		},
		"nested item ends the lead": {
			"- Top with no period\n  - Nested.",
			[]string{"- Top with no period"},
		},
		"headings and prose ignored": {
			"# Style\n\nSome prose. More prose.\n\n## Rules\n\n- One.\n\nProse between.\n\n### More\n\n- Two.\n",
			[]string{"- One.", "- Two."},
		},
		"code blocks ignored": {
			"```\n- not an item\n```\n\n- Real.\n\n~~~md\n1. also not\n~~~",
			[]string{"- Real."},
		},
		"a shorter fence inside a longer one stays code": {
			"````md\n```\n- not an item\n```\n- still code\n````\n\n- Real.",
			[]string{"- Real."},
		},
		"a fence with text after it doesn't close": {
			"```\n```not a close\n- still code\n```\n- Real.",
			[]string{"- Real."},
		},
		"a closing fence of the other character doesn't close": {
			"~~~\n```\n- still code\n~~~\n- Real.",
			[]string{"- Real."},
		},
		"a fence in an indented code example is text": {
			"- One.\n\nExample:\n\n    ```\n    - not a fence opener\n\n- Two.",
			[]string{"- One.", "- Two."},
		},
		"an item that opens with a fence": {
			"- ```\n  - not an item\n  ```\n- Real.",
			[]string{"- Real."},
		},
		"a nested item that opens with a fence": {
			"- **Top.** Body.\n  - ```\n    - not an item\n    ```\n- Next.",
			[]string{"- Top.", "- Next."},
		},
		"a fenced nested item indented as code opens no fence": {
			"- **Top.** Body.\n\n      - ```\n- Next.",
			[]string{"- Top.", "- Next."},
		},
		"an indented heading or break inside an item keeps it open": {
			"- **Top.** Body.\n\n  ## Inside\n\n  - Nested.\n\n  ---\n\n  - Nested too.\n- Next.",
			[]string{"- Top.", "- Next."},
		},
		"a #tag is text, not a heading": {
			"- Lead with\n#tag in it. Then more.",
			[]string{"- Lead with #tag in it."},
		},
		"thematic breaks are not bullets": {
			"---\n\n* * *\n\n- Real.",
			[]string{"- Real."},
		},
		"no list": {
			"Sign off as Marie. Keep it warm.",
			nil,
		},
		"CRLF": {
			"1. **One.** Body.\r\n2. **Two.** Body.\r\n",
			[]string{"1. One.", "2. Two."},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tc.want, instructionsBrief(tc.content))
		})
	}
}

func TestInstructionsBriefStripsControlsAndClips(t *testing.T) {
	brief := instructionsBrief("- **Be\u001b[2J brief.**\n- " + strings.Repeat("word ", 100))
	require.Len(t, brief, 2)
	assert.Equal(t, "- Be brief.", brief[0], "terminal controls are stripped")
	assert.LessOrEqual(t, len([]rune(brief[1])), instructionsBriefMaxRunes+2)
	assert.True(t, strings.HasSuffix(brief[1], "…"))
}

func TestInstructionsCommandJSONCarriesTheBrief(t *testing.T) {
	path := writeInstructions(t, "style.md", instructionsStyleFixture)
	app, buf := setupConfigTestApp(t)
	app.Config.Instructions = path

	_, _, err := runInstructionsCommand(t, app)
	require.NoError(t, err)
	var env struct {
		Data instructionsResult `json:"data"`
	}
	require.NoError(t, json.Unmarshal(buf.Bytes(), &env), buf.String())
	require.Len(t, env.Data.Documents, 1)
	assert.Len(t, env.Data.Documents[0].Brief, 11)
	assert.Equal(t, "1. Speak only when it helps someone.", env.Data.Documents[0].Brief[0])
	assert.Contains(t, env.Data.Documents[0].Content, "Post when asked", "the full text is still there")
}
