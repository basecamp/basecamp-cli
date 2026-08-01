package commands

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/richtext"
)

const notesPath = "/99999/my/notes.json"

func notesGetRoute(body string) stubRoute {
	return stubRoute{
		method: http.MethodGet,
		path:   notesPath,
		status: http.StatusOK,
		body:   body,
	}
}

func notesUpdateRoute() stubRoute {
	return stubRoute{
		method: http.MethodPut,
		path:   notesPath,
		status: http.StatusOK,
		body: `{
			"id": 42, "type": "Notebook::Note",
			"created_at": "2026-07-01T10:00:00.000Z",
			"updated_at": "2026-07-31T10:00:00.000Z",
			"content": "<div>hello</div>",
			"content_attachments": [],
			"url": "https://3.basecampapi.com/99999/my/notes.json",
			"app_url": "https://3.basecamp.com/99999/my/notes"
		}`,
	}
}

// Every account starts here: the note record does not exist until the first
// write, so id and the timestamps are null and content is "". That is an empty
// note, not a missing one, and must render rather than fail.
func TestNotesShowRendersThePreFirstWriteState(t *testing.T) {
	app, _, out := setupPersonalFeedApp(t, notesGetRoute(`{
		"id": null, "type": "Notebook::Note",
		"created_at": null, "updated_at": null,
		"content": "", "content_attachments": [],
		"url": "", "app_url": ""
	}`))

	err := executeRecordingCommand(NewNotesCmd(), app, "show")

	require.NoError(t, err, "an unwritten note is an empty note, not an error")
	var envelope struct {
		OK      bool   `json:"ok"`
		Summary string `json:"summary"`
	}
	require.NoError(t, json.Unmarshal(out.Bytes(), &envelope))
	assert.True(t, envelope.OK)
	assert.Contains(t, envelope.Summary, "empty")
	assert.Contains(t, envelope.Summary, "nothing written yet")
}

func TestNotesShowRendersAWrittenNote(t *testing.T) {
	app, _, out := setupPersonalFeedApp(t, notesGetRoute(`{
		"id": 42, "type": "Notebook::Note",
		"created_at": "2026-07-01T10:00:00.000Z",
		"updated_at": "2026-07-31T14:30:00.000Z",
		"content": "<div>hello</div>", "content_attachments": [],
		"url": "", "app_url": ""
	}`))

	require.NoError(t, executeRecordingCommand(NewNotesCmd(), app, "show"))

	var envelope struct {
		Summary string `json:"summary"`
	}
	require.NoError(t, json.Unmarshal(out.Bytes(), &envelope))
	assert.Contains(t, envelope.Summary, "last updated 2026-07-31")
}

// The note is a rich text field, so the body must go over the wire as HTML.
// Sending the raw Markdown would store escaped or malformed markup.
func TestNotesSetSendsMarkdownAsHTML(t *testing.T) {
	app, transport, _ := setupPersonalFeedApp(t, notesUpdateRoute())

	require.NoError(t, executeRecordingCommand(NewNotesCmd(), app, "set", "**bold** and a [link](https://example.com)"))

	call := transport.last(t)
	assert.Equal(t, http.MethodPut, call.Method)
	assert.Equal(t, notesPath, call.Path)

	var body struct {
		Note struct {
			Content string `json:"content"`
		} `json:"note"`
	}
	require.NoError(t, json.Unmarshal([]byte(call.Body), &body))
	assert.Equal(t, richtext.MarkdownToHTML("**bold** and a [link](https://example.com)"), body.Note.Content)
	assert.Contains(t, body.Note.Content, "<strong>bold</strong>")
	assert.NotContains(t, body.Note.Content, "**bold**", "raw Markdown must not reach the wire")
}

func TestNotesSetReadsFromAFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "note.md")
	require.NoError(t, os.WriteFile(path, []byte("# Heading\n\nBody text."), 0o600))

	app, transport, _ := setupPersonalFeedApp(t, notesUpdateRoute())

	require.NoError(t, executeRecordingCommand(NewNotesCmd(), app, "set", "--file", path))

	var body struct {
		Note struct {
			Content string `json:"content"`
		} `json:"note"`
	}
	require.NoError(t, json.Unmarshal([]byte(transport.last(t).Body), &body))
	assert.Contains(t, body.Note.Content, "Heading")
	assert.NotContains(t, body.Note.Content, "# Heading", "raw Markdown must not reach the wire")
}

func TestNotesSetReadsPipedStdin(t *testing.T) {
	app, transport, _ := setupPersonalFeedApp(t, notesUpdateRoute())

	cmd := NewNotesCmd()
	cmd.SetIn(strings.NewReader("piped note body"))

	require.NoError(t, executeRecordingCommand(cmd, app, "set"))

	var body struct {
		Note struct {
			Content string `json:"content"`
		} `json:"note"`
	}
	require.NoError(t, json.Unmarshal([]byte(transport.last(t).Body), &body))
	assert.Contains(t, body.Note.Content, "piped note body")
}

// set replaces the whole note, so the failure modes that would silently erase
// it are rejected before the request rather than written through.
func TestNotesSetRejectsAmbiguousOrEmptyInput(t *testing.T) {
	emptyFile := filepath.Join(t.TempDir(), "empty.md")
	require.NoError(t, os.WriteFile(emptyFile, []byte("   \n"), 0o600))

	populated := filepath.Join(t.TempDir(), "note.md")
	require.NoError(t, os.WriteFile(populated, []byte("content"), 0o600))

	for _, tc := range []struct {
		name string
		args []string
	}{
		{"argument and --file together", []string{"set", "inline", "--file", populated}},
		{"an empty file", []string{"set", "--file", emptyFile}},
		{"a missing file", []string{"set", "--file", filepath.Join(t.TempDir(), "nope.md")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app, transport, _ := setupPersonalFeedApp(t, notesUpdateRoute())

			err := executeRecordingCommand(NewNotesCmd(), app, tc.args...)

			requireBookmarksUsageError(t, err)
			assert.Empty(t, transport.recorded(), "a rejected write must not reach the server")
		})
	}
}
