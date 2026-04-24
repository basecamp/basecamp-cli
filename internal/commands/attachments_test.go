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
	"github.com/basecamp/basecamp-cli/internal/richtext"
)

// ---------------------------------------------------------------------------
// Tests from PR #296 (attachments list)
// ---------------------------------------------------------------------------

func TestResolveAttachmentTarget(t *testing.T) {
	tests := []struct {
		name     string
		arg      string
		typeHint string
		wantID   string
		wantType string
	}{
		{
			name:     "comment URL prefers CommentID",
			arg:      "https://3.basecamp.com/123/buckets/456/todos/111#__recording_789",
			wantID:   "789",
			wantType: "comment",
		},
		{
			name:     "comment URL with explicit --type comment",
			arg:      "https://3.basecamp.com/123/buckets/456/todos/111#__recording_789",
			typeHint: "comment",
			wantID:   "789",
			wantType: "comment",
		},
		{
			name:     "comment URL with explicit --type todo uses RecordingID",
			arg:      "https://3.basecamp.com/123/buckets/456/todos/111#__recording_789",
			typeHint: "todo",
			wantID:   "111",
			wantType: "todo",
		},
		{
			name:     "plain URL without comment fragment",
			arg:      "https://3.basecamp.com/123/buckets/456/todos/111",
			wantID:   "111",
			wantType: "todos",
		},
		{
			name:     "plain ID with no type",
			arg:      "42",
			wantID:   "42",
			wantType: "",
		},
		{
			name:     "plain ID with explicit type",
			arg:      "42",
			typeHint: "message",
			wantID:   "42",
			wantType: "message",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id, typ := resolveAttachmentTarget(tt.arg, tt.typeHint)
			assert.Equal(t, tt.wantID, id)
			assert.Equal(t, tt.wantType, typ)
		})
	}
}

func TestTypeToEndpointAnswerAliases(t *testing.T) {
	assert.Equal(t, "/question_answers/42.json", typeToEndpoint("answer", "42"))
	assert.Equal(t, "/question_answers/42.json", typeToEndpoint("question_answers", "42"))
	assert.Equal(t, "", typeToEndpoint("question_answer", "42"))
}

func TestNormalizeShowType(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"", ""},
		{"todo", "todo"},
		{"todos", "todo"},
		{"question_answers", ""},
		{"answer", ""},
		{"questions", "checkin"},
		{"schedule_entries", "schedule-entry"},
		{"card_tables", "card-table"},
		{"recording", ""},
		{"recordings", ""},
		{"comment", "comment"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			assert.Equal(t, tt.expected, normalizeShowType(tt.input))
		})
	}
}

func TestTypeToEndpointKnownTypes(t *testing.T) {
	tests := []struct {
		typ      string
		expected string
	}{
		{"todo", "/todos/1.json"},
		{"comment", "/comments/1.json"},
		{"message", "/messages/1.json"},
		{"document", "/documents/1.json"},
		{"upload", "/uploads/1.json"},
		{"forward", "/forwards/1.json"},
		{"bogus", ""},
	}
	for _, tt := range tests {
		t.Run(tt.typ, func(t *testing.T) {
			assert.Equal(t, tt.expected, typeToEndpoint(tt.typ, "1"))
		})
	}
}

func TestIsGenericType(t *testing.T) {
	assert.True(t, isGenericType(""))
	assert.True(t, isGenericType("recording"))
	assert.True(t, isGenericType("recordings"))
	assert.True(t, isGenericType("lines"))
	assert.True(t, isGenericType("line"))
	assert.True(t, isGenericType("replies"))
	assert.False(t, isGenericType("todo"))
	assert.False(t, isGenericType("message"))
}

// ---------------------------------------------------------------------------
// Tests for attachments download
// ---------------------------------------------------------------------------

func TestUniqueFilename(t *testing.T) {
	t.Run("no collision", func(t *testing.T) {
		used := make(map[string]bool)
		name := uniqueFilename("/tmp/nonexistent-dir-xyz", used, "report.pdf")
		assert.Equal(t, "report.pdf", name)
	})

	t.Run("used name gets suffix", func(t *testing.T) {
		used := map[string]bool{"report.pdf": true}
		name := uniqueFilename("/tmp/nonexistent-dir-xyz", used, "report.pdf")
		assert.Equal(t, "report-1.pdf", name)
	})

	t.Run("multiple same name", func(t *testing.T) {
		used := map[string]bool{"report.pdf": true, "report-1.pdf": true}
		name := uniqueFilename("/tmp/nonexistent-dir-xyz", used, "report.pdf")
		assert.Equal(t, "report-2.pdf", name)
	})

	t.Run("empty name defaults to download", func(t *testing.T) {
		used := make(map[string]bool)
		name := uniqueFilename("/tmp/nonexistent-dir-xyz", used, "")
		assert.Equal(t, "download", name)
	})

	t.Run("disk collision", func(t *testing.T) {
		dir := t.TempDir()
		f, err := os.Create(filepath.Join(dir, "photo.jpg"))
		require.NoError(t, err)
		f.Close()

		used := make(map[string]bool)
		name := uniqueFilename(dir, used, "photo.jpg")
		assert.Equal(t, "photo-1.jpg", name)
	})

	t.Run("path traversal stripped", func(t *testing.T) {
		used := make(map[string]bool)
		name := uniqueFilename("/tmp", used, "../../../etc/passwd")
		assert.Equal(t, "passwd", name)
	})
}

func TestWithAttachmentMeta(t *testing.T) {
	t.Run("adds field-scoped key to struct", func(t *testing.T) {
		type sample struct {
			Title string `json:"title"`
		}
		data := sample{Title: "test"}
		atts := []richtext.ParsedAttachment{
			{URL: "https://example.com/a.png", Filename: "a.png", ContentType: "image/png"},
		}
		result := withAttachmentMeta(data, "content", atts, nil)
		m, ok := result.(map[string]any)
		require.True(t, ok)
		assert.Equal(t, "test", m["title"])
		assert.NotNil(t, m["content_attachments"])
		attachments := m["content_attachments"].([]map[string]string)
		assert.Len(t, attachments, 1)
		assert.Equal(t, "a.png", attachments[0]["filename"])
		assert.Equal(t, "https://example.com/a.png", attachments[0]["url"])
	})

	t.Run("empty attachments returns original", func(t *testing.T) {
		data := map[string]string{"title": "test"}
		result := withAttachmentMeta(data, "content", nil, nil)
		assert.Equal(t, data, result)
	})

	t.Run("merges download results", func(t *testing.T) {
		type sample struct {
			Title string `json:"title"`
		}
		data := sample{Title: "test"}
		atts := []richtext.ParsedAttachment{
			{URL: "https://example.com/a.png", Filename: "a.png"},
			{URL: "https://example.com/b.png", Filename: "b.png"},
		}
		dlResults := []attachmentResult{
			{Path: "/tmp/a.png", Status: "downloaded"},
			{Filename: "b.png", Status: "error", Error: "not found"},
		}
		result := withAttachmentMeta(data, "description", atts, dlResults)
		m := result.(map[string]any)
		attachments := m["description_attachments"].([]map[string]string)
		assert.Equal(t, "/tmp/a.png", attachments[0]["path"])
		assert.Equal(t, "downloaded", attachments[0]["download_status"])
		assert.Equal(t, "error", attachments[1]["download_status"])
		assert.Equal(t, "not found", attachments[1]["download_error"])
	})

	t.Run("sequential calls produce both keys", func(t *testing.T) {
		type sample struct {
			Title string `json:"title"`
		}
		data := sample{Title: "card"}
		contentAtts := []richtext.ParsedAttachment{
			{URL: "https://example.com/c.png", Filename: "c.png"},
		}
		descAtts := []richtext.ParsedAttachment{
			{URL: "https://example.com/d.png", Filename: "d.png"},
		}
		result := withAttachmentMeta(data, "content", contentAtts, nil)
		result = withAttachmentMeta(result, "description", descAtts, nil)
		m := result.(map[string]any)
		assert.Equal(t, "card", m["title"])
		assert.NotNil(t, m["content_attachments"])
		assert.NotNil(t, m["description_attachments"])
		cAtts := m["content_attachments"].([]map[string]string)
		dAtts := m["description_attachments"].([]map[string]string)
		assert.Equal(t, "c.png", cAtts[0]["filename"])
		assert.Equal(t, "d.png", dAtts[0]["filename"])
	})

	t.Run("preserves native attachments key", func(t *testing.T) {
		// CampfireLine records have a native API "attachments" array.
		// Field-scoped keys must not collide with it.
		data := map[string]any{
			"id":          float64(42),
			"content":     "<p>see attached</p>",
			"attachments": []any{map[string]any{"sgid": "native-sgid"}},
		}
		atts := []richtext.ParsedAttachment{
			{URL: "https://example.com/a.png", Filename: "a.png"},
		}
		result := withAttachmentMeta(data, "content", atts, nil)
		m := result.(map[string]any)
		// Native API field untouched
		native := m["attachments"].([]any)
		assert.Len(t, native, 1)
		// Field-scoped collection added separately
		scoped := m["content_attachments"].([]map[string]string)
		assert.Len(t, scoped, 1)
		assert.Equal(t, "a.png", scoped[0]["filename"])
	})

	t.Run("preserves integer precision through marshal round-trip", func(t *testing.T) {
		// IDs above 2^53 must survive the struct → map conversion.
		type sample struct {
			ID    int64  `json:"id"`
			Title string `json:"title"`
		}
		data := sample{ID: 9007199254740993, Title: "big-id"} // 2^53 + 1
		atts := []richtext.ParsedAttachment{
			{URL: "https://example.com/a.png", Filename: "a.png"},
		}
		result := withAttachmentMeta(data, "content", atts, nil)
		m := result.(map[string]any)
		// json.Number preserves the exact string representation
		idNum, ok := m["id"].(json.Number)
		require.True(t, ok, "id should be json.Number, got %T", m["id"])
		assert.Equal(t, "9007199254740993", idNum.String())
	})
}

func TestAttachmentMeta(t *testing.T) {
	t.Run("uses DisplayURL", func(t *testing.T) {
		atts := []richtext.ParsedAttachment{
			{URL: "https://preview.example.com/a-preview", Href: "https://storage.example.com/download/a.png", Filename: "a.png", ContentType: "image/png", Filesize: "1024"},
			{URL: "https://preview.example.com/b-preview"},
		}
		result := attachmentMeta(atts, nil)
		assert.Len(t, result, 2)
		assert.Equal(t, "a.png", result[0]["filename"])
		assert.Equal(t, "image/png", result[0]["content_type"])
		assert.Equal(t, "1024", result[0]["filesize"])
		assert.Equal(t, "https://storage.example.com/download/a.png", result[0]["url"])
		assert.Equal(t, "https://preview.example.com/b-preview", result[1]["url"])
		_, hasFilename := result[1]["filename"]
		assert.False(t, hasFilename)
	})

	t.Run("surfaces width and height", func(t *testing.T) {
		atts := []richtext.ParsedAttachment{
			{URL: "https://example.com/img.jpg", Filename: "img.jpg", ContentType: "image/jpeg", Width: "1920", Height: "1080"},
		}
		result := attachmentMeta(atts, nil)
		assert.Equal(t, "1920", result[0]["width"])
		assert.Equal(t, "1080", result[0]["height"])
	})
}

func TestDownloadableAttachments(t *testing.T) {
	atts := []richtext.ParsedAttachment{
		{URL: "https://example.com/a.png", Filename: "a.png"},
		{Filename: "no-url.png"},
		{Href: "https://example.com/b.txt", Filename: "b.txt"},
	}
	result := downloadableAttachments(atts)
	assert.Len(t, result, 2)
	assert.Equal(t, "a.png", result[0].Filename)
	assert.Equal(t, "b.txt", result[1].Filename)
}

func TestAttachmentBreadcrumb(t *testing.T) {
	t.Run("single", func(t *testing.T) {
		bc := attachmentBreadcrumb("123", 1)
		assert.Equal(t, "download", bc.Action)
		assert.Equal(t, "basecamp attachments download 123", bc.Cmd)
		assert.Equal(t, "Download attachment", bc.Description)
	})

	t.Run("multiple", func(t *testing.T) {
		bc := attachmentBreadcrumb("456", 3)
		assert.Equal(t, "Download 3 attachments", bc.Description)
	})
}

func TestExtractContentField(t *testing.T) {
	t.Run("HTML content field", func(t *testing.T) {
		data := map[string]any{"content": "<p>hello</p>", "title": "test"}
		assert.Equal(t, "<p>hello</p>", extractContentField(data))
	})

	t.Run("HTML description field", func(t *testing.T) {
		data := map[string]any{"description": "<p>desc</p>", "title": "test"}
		assert.Equal(t, "<p>desc</p>", extractContentField(data))
	})

	t.Run("both HTML concatenates", func(t *testing.T) {
		data := map[string]any{"content": "<p>content</p>", "description": "<p>desc</p>"}
		result := extractContentField(data)
		assert.Contains(t, result, "<p>content</p>")
		assert.Contains(t, result, "<p>desc</p>")
	})

	t.Run("neither present", func(t *testing.T) {
		data := map[string]any{"title": "test"}
		assert.Equal(t, "", extractContentField(data))
	})

	t.Run("empty string ignored", func(t *testing.T) {
		data := map[string]any{"content": "", "description": "<p>desc</p>"}
		assert.Equal(t, "<p>desc</p>", extractContentField(data))
	})

	t.Run("plain content with HTML description prefers description", func(t *testing.T) {
		// Todos: content is plain-text title, description has the rich body
		data := map[string]any{
			"content":     "Buy groceries",
			"description": `<p>See <bc-attachment href="https://storage.example.com/a.png" filename="list.png"></bc-attachment></p>`,
		}
		result := extractContentField(data)
		assert.Contains(t, result, "bc-attachment")
		assert.NotContains(t, result, "Buy groceries")
	})

	t.Run("HTML content with plain description prefers content", func(t *testing.T) {
		data := map[string]any{
			"content":     "<p>Rich message body</p>",
			"description": "plain text summary",
		}
		assert.Equal(t, "<p>Rich message body</p>", extractContentField(data))
	})
}

func TestNewAttachmentsCmd(t *testing.T) {
	cmd := NewAttachmentsCmd()
	assert.Equal(t, "attachments", cmd.Use)
	assert.Nil(t, cmd.RunE)

	// Has list subcommand
	sub, _, err := cmd.Find([]string{"list"})
	require.NoError(t, err)
	assert.Equal(t, "list", sub.Name())

	// Has download subcommand
	sub, _, err = cmd.Find([]string{"download"})
	require.NoError(t, err)
	assert.Equal(t, "download", sub.Name())

	// Download flags present
	assert.NotNil(t, sub.Flags().Lookup("out"))
	assert.NotNil(t, sub.Flags().Lookup("file"))
	assert.NotNil(t, sub.Flags().Lookup("index"))
	assert.NotNil(t, sub.Flags().Lookup("type"))
}

func TestFilterParsedAttachments(t *testing.T) {
	atts := []richtext.ParsedAttachment{
		{Href: "https://example.com/a.png", Filename: "a.png"},
		{Href: "https://example.com/b.txt", Filename: "b.txt"},
		{Href: "https://example.com/a2.png", Filename: "a.png"},
	}

	t.Run("matches by name", func(t *testing.T) {
		result := filterParsedAttachments(atts, "a.png")
		assert.Len(t, result, 2)
	})

	t.Run("no match", func(t *testing.T) {
		result := filterParsedAttachments(atts, "nope.zip")
		assert.Empty(t, result)
	})
}

func TestParsedAttachmentFilenames(t *testing.T) {
	atts := []richtext.ParsedAttachment{
		{Filename: "a.png"},
		{Filename: "b.txt"},
		{Filename: "a.png"},
		{Filename: ""},
	}
	names := parsedAttachmentFilenames(atts)
	assert.Equal(t, []string{"a.png", "b.txt", "(unnamed)"}, names)
}

func TestWriteBodyToFile(t *testing.T) {
	t.Run("writes exact filename", func(t *testing.T) {
		dir := t.TempDir()
		body := strings.NewReader("hello world")
		path, written, err := writeBodyToFile(body, dir, "test.txt")
		require.NoError(t, err)
		assert.Equal(t, int64(11), written)
		assert.Equal(t, filepath.Join(dir, "test.txt"), path)

		content, err := os.ReadFile(path)
		require.NoError(t, err)
		assert.Equal(t, "hello world", string(content))
	})

	t.Run("rejects path traversal", func(t *testing.T) {
		dir := t.TempDir()
		body := strings.NewReader("data")
		_, _, err := writeBodyToFile(body, dir, "../escape.txt")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "path traversal")
	})
}

// TestFetchItemContentRefetchesLineWithLargeParentID verifies that
// fetchItemContent uses UseNumber when decoding the recording response,
// so parentRecordingID can build the correct refetch endpoint even when
// the parent ID exceeds 2^53.
func TestFetchItemContentRefetchesLineWithLargeParentID(t *testing.T) {
	largeID := "9007199254740993" // 2^53 + 1

	transport := &showTrackingTransport{
		responder: func(path string) (int, string) {
			if strings.Contains(path, "/recordings/") {
				return 200, `{"id": 111, "type": "Chat::Lines::Text", "content": "sparse",` +
					`"parent": {"id": ` + largeID + `, "type": "Chat::Transcript"}}`
			}
			if strings.Contains(path, "/chats/"+largeID+"/lines/111") {
				return 200, `{"id": 111, "type": "Chat::Lines::Text",` +
					`"content": "<p>Rich <bc-attachment url=\"https://example.com/a.png\" filename=\"a.png\"></bc-attachment></p>"}`
			}
			return 200, `{}`
		},
	}
	app := showTestApp(t, transport)

	cmd := NewAttachmentsCmd()
	cmd.SetArgs([]string{"list", "111"})
	ctx := appctx.WithApp(context.Background(), app)
	cmd.SetContext(ctx)
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})

	err := cmd.Execute()
	require.NoError(t, err)

	reqs := transport.getRequests()
	require.Len(t, reqs, 2, "expected 2 requests: /recordings/ then /chats/{largeParentId}/lines/")
	assert.Contains(t, reqs[0], "/recordings/111.json")
	assert.Contains(t, reqs[1], "/chats/"+largeID+"/lines/111.json")
}
