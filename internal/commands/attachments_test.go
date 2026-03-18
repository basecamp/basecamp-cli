package commands

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/basecamp/basecamp-cli/internal/urlarg"
)

func TestAttachmentsCommentURLPrefersCommentID(t *testing.T) {
	// A comment URL with a #__recording_NNN fragment should resolve to the
	// comment's recording ID, not the parent item's recording ID.
	parsed := urlarg.Parse("https://3.basecamp.com/123/buckets/456/todos/111#__recording_789")
	assert.NotNil(t, parsed)

	// Simulate the command's URL resolution logic: prefer CommentID.
	var id, recordType string
	if parsed.CommentID != "" {
		id = parsed.CommentID
		recordType = "comment"
	} else if parsed.RecordingID != "" {
		id = parsed.RecordingID
	}

	assert.Equal(t, "789", id)
	assert.Equal(t, "comment", recordType)
}

func TestAttachmentsCommentURLWithExplicitType(t *testing.T) {
	// When --type is something other than comment, CommentID should NOT
	// override the recording ID (avoids type/ID mismatch).
	parsed := urlarg.Parse("https://3.basecamp.com/123/buckets/456/todos/111#__recording_789")
	assert.NotNil(t, parsed)

	recordType := "todo" // explicit --type todo
	var id string
	if parsed.CommentID != "" && (recordType == "" || recordType == "comment" || recordType == "comments") {
		id = parsed.CommentID
		recordType = "comment"
	} else if parsed.RecordingID != "" {
		id = parsed.RecordingID
	}

	assert.Equal(t, "111", id)
	assert.Equal(t, "todo", recordType)
}

func TestAttachmentsURLWithoutComment(t *testing.T) {
	parsed := urlarg.Parse("https://3.basecamp.com/123/buckets/456/todos/111")
	assert.NotNil(t, parsed)
	assert.Equal(t, "", parsed.CommentID)
	assert.Equal(t, "111", parsed.RecordingID)
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
		{"question_answers", "checkin"},
		{"answer", "checkin"},
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
