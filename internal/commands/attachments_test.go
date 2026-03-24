package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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

func TestWithInlineAttachments(t *testing.T) {
	t.Run("adds field to struct", func(t *testing.T) {
		type sample struct {
			Title string `json:"title"`
		}
		data := sample{Title: "test"}
		atts := []richtext.InlineAttachment{
			{Href: "https://example.com/a.png", Filename: "a.png", ContentType: "image/png"},
		}
		result := withInlineAttachments(data, atts)
		m, ok := result.(map[string]any)
		require.True(t, ok)
		assert.Equal(t, "test", m["title"])
		assert.NotNil(t, m["inline_attachments"])
		attachments := m["inline_attachments"].([]map[string]string)
		assert.Len(t, attachments, 1)
		assert.Equal(t, "a.png", attachments[0]["filename"])
	})

	t.Run("empty attachments returns original", func(t *testing.T) {
		data := map[string]string{"title": "test"}
		result := withInlineAttachments(data, nil)
		assert.Equal(t, data, result)
	})
}

func TestInlineAttachmentMeta(t *testing.T) {
	atts := []richtext.InlineAttachment{
		{Href: "https://example.com/a.png", Filename: "a.png", ContentType: "image/png", Filesize: "1024"},
		{Href: "https://example.com/b.txt"},
	}
	result := inlineAttachmentMeta(atts)
	assert.Len(t, result, 2)
	assert.Equal(t, "a.png", result[0]["filename"])
	assert.Equal(t, "image/png", result[0]["content_type"])
	assert.Equal(t, "1024", result[0]["filesize"])
	assert.Equal(t, "https://example.com/b.txt", result[1]["href"])
	_, hasFilename := result[1]["filename"]
	assert.False(t, hasFilename)
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
