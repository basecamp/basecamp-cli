package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/appctx"
)

// Rich-text conversion is decided in one place. A command converting content
// itself would ignore --format; chat keeps its own --content-type contract.
func TestRichTextConversionGoesThroughOneHelper(t *testing.T) {
	files, err := filepath.Glob("*.go")
	require.NoError(t, err)
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") || file == "richtext_format.go" || file == "chat.go" {
			continue
		}
		src, err := os.ReadFile(file)
		require.NoError(t, err)
		assert.NotContains(t, string(src), "richtext.MarkdownToHTML(",
			"%s converts rich text directly; use richTextToHTML so --format applies", file)
	}
}

func runCommentWithStderr(t *testing.T, transport *mockCommentWriteTransport, stdin string, args ...string) (string, error) {
	t.Helper()
	app, _ := setupCommentsWriteTestApp(t, transport)
	cmd := NewCommentsCmd()
	cmd.SetIn(strings.NewReader(stdin))
	stderr := &bytes.Buffer{}
	cmd.SetContext(appctx.WithApp(context.Background(), app))
	cmd.SetArgs(args)
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(stderr)
	err := cmd.Execute()
	return stderr.String(), err
}

func sentContent(t *testing.T, transport *mockCommentWriteTransport) string {
	t.Helper()
	require.Len(t, transport.capturedBodies, 1)
	var body map[string]string
	require.NoError(t, json.Unmarshal(transport.capturedBodies[0], &body))
	return body["content"]
}

// The report this contract fixes: Markdown beside a supported tag posted as
// literal Markdown, because the tag made the whole body pass as HTML.
func TestCommentsCreateConvertsMarkdownBesideAnInlineTable(t *testing.T) {
	transport := &mockCommentWriteTransport{}
	input := "## Local measurements\n\n" +
		"Both branches are off `five`. Treat them as **provisional**.\n\n" +
		"<table>\n<tr><td>LCP</td><td>218ms</td></tr>\n</table>\n\n" +
		"- [#10589](https://github.com/basecamp/bc3/pull/10589)\n"

	_, err := runCommentWithStderr(t, transport, input, "create", "789", "-")
	require.NoError(t, err)

	got := sentContent(t, transport)
	assert.Contains(t, got, "<h2>Local measurements</h2>")
	assert.Contains(t, got, "<code>five</code>")
	assert.Contains(t, got, "<strong>provisional</strong>")
	assert.Contains(t, got, "<table>\n<tr><td>LCP</td><td>218ms</td></tr>\n</table>")
	assert.Contains(t, got, `<a href="https://github.com/basecamp/bc3/pull/10589">#10589</a>`)
	assert.NotContains(t, got, "## Local")
	assert.NotContains(t, got, "**provisional**")
}

func TestCommentsCreateFormatHTMLSendsContentExactly(t *testing.T) {
	transport := &mockCommentWriteTransport{}
	// Every part of this would change if it were read as Markdown: the
	// adjacent paragraphs would gain a separator, the indented line would
	// become a code block, and the asterisks would become <strong>.
	// Reading stdin drops the trailing newline, as it does for every format.
	input := "<p>A</p><p>B</p>\n\n    <em>indented</em>\n**not bold** `not code`"

	stderr, err := runCommentWithStderr(t, transport, input+"\n", "create", "789", "-", "--format", "html")
	require.NoError(t, err)
	assert.Equal(t, input, sentContent(t, transport))
	assert.Empty(t, stderr)
}

func TestCommentsUpdateFormatHTMLSendsContentExactly(t *testing.T) {
	transport := &mockCommentWriteTransport{}
	input := "<h2>Plan</h2><p>**kept**</p>"

	_, err := runCommentWithStderr(t, transport, "", "update", "1234", input, "--format", "html")
	require.NoError(t, err)
	assert.Equal(t, input, sentContent(t, transport))
}

func TestCommentsCreateFormatMarkdownConvertsHTMLLookingInput(t *testing.T) {
	transport := &mockCommentWriteTransport{}

	_, err := runCommentWithStderr(t, transport, "", "create", "789", "<p>Intro</p>\n\n**bold**", "--format", "markdown")
	require.NoError(t, err)
	got := sentContent(t, transport)
	assert.Contains(t, got, "<p>Intro</p>")
	assert.Contains(t, got, "<strong>bold</strong>")
}

func TestCommentsCreateRejectsUnknownFormat(t *testing.T) {
	transport := &mockCommentWriteTransport{}

	_, err := runCommentWithStderr(t, transport, "", "create", "789", "hi", "--format", "text")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `"markdown" or "html"`)
	assert.Empty(t, transport.capturedBodies)
}

// Content that used to be sent as written because it looked like HTML is now
// read as Markdown. The caller is told once, on stderr, how to keep the old
// result — unless the format was chosen explicitly, or the only tags are the
// mention and attachment markup Markdown content carries routinely.
func TestRichTextHTMLInMarkdownWarns(t *testing.T) {
	tests := []struct {
		name  string
		args  []string
		warns bool
	}{
		{"html read as markdown by default", []string{"create", "789", "<p>A</p><p>B</p>"}, true},
		{"inline tag in markdown", []string{"create", "789", "Some <strong>bold</strong>"}, true},
		{"explicit markdown", []string{"create", "789", "<p>A</p>", "--format", "markdown"}, false},
		{"explicit html", []string{"create", "789", "<p>A</p>", "--format", "html"}, false},
		{"plain markdown", []string{"create", "789", "**bold** and `<p>` in code"}, false},
		{"mention markup only", []string{"create", "789", `Hi <bc-attachment sgid="X" content-type="application/vnd.basecamp.mention"></bc-attachment>`}, false},
		{"uppercase tag", []string{"create", "789", "<P>A</P>"}, true},
		{"table row tags", []string{"create", "789", "Row: <tr><td>x</td></tr>"}, true},
		{"underline tag", []string{"create", "789", "Some <u>underline</u>"}, true},
		{"html in a tilde fence", []string{"create", "789", "~~~\n<p>x</p>\n~~~"}, false},
		{"html in indented code", []string{"create", "789", "Example:\n\n    <p>x</p>"}, false},
		{"angle brackets in prose", []string{"create", "789", "A Vec<String> here"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			transport := &mockCommentWriteTransport{}
			stderr, err := runCommentWithStderr(t, transport, "", tt.args...)
			require.NoError(t, err)
			if tt.warns {
				assert.Contains(t, stderr, "--format html")
				assert.Equal(t, 1, strings.Count(stderr, "\n"), "one warning line: %q", stderr)
			} else {
				assert.Empty(t, stderr)
			}
		})
	}
}

// passthroughHTML would change under Markdown: the adjacent paragraphs gain a
// separator and the asterisks become <strong>.
const passthroughHTML = "<p>A</p><p>B</p>\n**x**"

func capturedField(t *testing.T, raw []byte, field string) string {
	t.Helper()
	require.NotEmpty(t, raw)
	var body map[string]any
	require.NoError(t, json.Unmarshal(raw, &body))
	value, ok := body[field].(string)
	require.True(t, ok, "%s missing from %s", field, raw)
	return value
}

func TestFormatHTMLSendsEveryRichTextFieldExactly(t *testing.T) {
	t.Run("cards create", func(t *testing.T) {
		transport := &mockCardCreateTransport{}
		app := setupCardsMockApp(t, transport)
		require.NoError(t, executeCommand(NewCardsCmd(), app, "create", "Title", passthroughHTML, "--column", "12345", "--format", "html"))
		assert.Equal(t, passthroughHTML, capturedField(t, transport.capturedBody, "content"))
	})
	t.Run("cards update", func(t *testing.T) {
		transport := &mockCardCreateTransport{}
		app := setupCardsMockApp(t, transport)
		require.NoError(t, executeCommand(NewCardsCmd(), app, "update", "999", "--body", passthroughHTML, "--format", "html"))
		assert.Equal(t, passthroughHTML, capturedField(t, transport.capturedBody, "content"))
	})
	t.Run("todos update", func(t *testing.T) {
		transport := &mockTodoUpdateTransport{}
		app := setupTodoUpdateApp(t, transport)
		require.NoError(t, executeTodosCommand(NewTodosCmd(), app, "update", "999", "--description", passthroughHTML, "--format", "html"))
		assert.Equal(t, passthroughHTML, capturedField(t, transport.capturedBody, "description"))
	})
	t.Run("schedule create", func(t *testing.T) {
		transport := &mockScheduleCreateTransport{}
		app, _ := setupMessagesMockApp(t, transport)
		require.NoError(t, executeMessagesCommand(NewScheduleCmd(), app, "create", "Event",
			"--starts-at", "2026-03-04T09:00:00Z", "--ends-at", "2026-03-04T09:30:00Z",
			"--description", passthroughHTML, "--format", "html"))
		assert.Equal(t, passthroughHTML, capturedField(t, transport.capturedBody, "description"))
	})
	t.Run("schedule update", func(t *testing.T) {
		transport := &mockScheduleCreateTransport{}
		app, _ := setupMessagesMockApp(t, transport)
		require.NoError(t, executeMessagesCommand(NewScheduleCmd(), app, "update", "999", "--description", passthroughHTML, "--format", "html"))
		assert.Equal(t, passthroughHTML, capturedField(t, transport.capturedBody, "description"))
	})
	t.Run("messages create from stdin", func(t *testing.T) {
		transport := &mockMessageCreateTransport{}
		app, _ := setupMessagesMockApp(t, transport)
		cmd := NewMessagesCmd()
		cmd.SetIn(strings.NewReader(passthroughHTML + "\n"))
		require.NoError(t, executeMessagesCommand(cmd, app, "create", "Title", "-", "--format", "html"))
		assert.Equal(t, passthroughHTML, capturedField(t, transport.capturedBody, "content"))
	})
	t.Run("files update upload", func(t *testing.T) {
		transport := &mockFilesUploadUpdateTransport{}
		app := showTestApp(t, transport)
		app.Config.ProjectID = "456"
		require.NoError(t, executeMessagesCommand(NewFilesCmd(), app, "update", "999", "--type", "upload", "--content", passthroughHTML, "--format", "html"))
		assert.Equal(t, passthroughHTML, capturedField(t, transport.capturedBody, "description"))
	})
	t.Run("files update document", func(t *testing.T) {
		transport := &mockFilesUpdateTransport{}
		app := showTestApp(t, transport)
		app.Config.ProjectID = "456"
		require.NoError(t, executeMessagesCommand(NewFilesCmd(), app, "update", "999", "--content", passthroughHTML, "--format", "html"))
		assert.Equal(t, passthroughHTML, capturedField(t, transport.capturedBody, "content"))
	})
}

// An upload description is rich text like any other, so a local image in it is
// uploaded rather than sent as a broken path.
func TestFilesUpdateUploadDescriptionResolvesLocalImages(t *testing.T) {
	transport := &mockFilesUploadUpdateTransport{}
	app := showTestApp(t, transport)
	app.Config.ProjectID = "456"
	err := executeMessagesCommand(NewFilesCmd(), app, "update", "999", "--type", "upload", "--content", "![alt](./missing.png)")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing.png")
	assert.Empty(t, transport.capturedBody)
}

func TestCheckinsAnswerCreateFormatHTMLSendsContentExactly(t *testing.T) {
	transport := &mockCheckinsAnswerCreateTransport{}
	app, _ := newTestAppWithTransport(t, transport)
	app.Config.ProjectID = "123"
	project := ""
	require.NoError(t, executeCommand(newCheckinsAnswerCreateCmd(&project), app, "456", passthroughHTML, "--format", "html"))
	assert.Equal(t, passthroughHTML, transport.recordedBody["content"])
}

func TestNotesSetFormatHTMLSendsContentExactly(t *testing.T) {
	app, transport, _ := setupPersonalFeedApp(t, notesUpdateRoute())

	require.NoError(t, executeRecordingCommand(NewNotesCmd(), app, "set", passthroughHTML, "--format", "html"))

	var body struct {
		Note struct {
			Content string `json:"content"`
		} `json:"note"`
	}
	require.NoError(t, json.Unmarshal([]byte(transport.last(t).Body), &body))
	assert.Equal(t, passthroughHTML, body.Note.Content)
}
