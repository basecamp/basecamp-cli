package commands

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

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
