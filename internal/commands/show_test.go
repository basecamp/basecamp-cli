package commands

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRecordingTypeEndpoint(t *testing.T) {
	tests := []struct {
		apiType  string
		id       string
		expected string
	}{
		{"Todo", "123", "/todos/123.json"},
		{"Todolist", "456", "/todolists/456.json"},
		{"Message", "789", "/messages/789.json"},
		{"Comment", "100", "/comments/100.json"},
		{"Kanban::Card", "200", "/card_tables/cards/200.json"},
		{"Document", "300", "/documents/300.json"},
		{"Vault::Document", "301", "/documents/301.json"},
		{"Schedule::Entry", "400", "/schedule_entries/400.json"},
		{"Question::Answer", "500", "/question_answers/500.json"},
		{"Inbox::Forward", "600", "/forwards/600.json"},
		{"Upload", "700", "/uploads/700.json"},
	}

	for _, tt := range tests {
		t.Run(tt.apiType, func(t *testing.T) {
			data := map[string]any{"type": tt.apiType}
			result := recordingTypeEndpoint(data, tt.id)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestRecordingTypeEndpoint_UnknownType(t *testing.T) {
	data := map[string]any{"type": "SomeNewType"}
	result := recordingTypeEndpoint(data, "999")
	assert.Equal(t, "", result, "unknown types should return empty string")
}

func TestRecordingTypeEndpoint_MissingType(t *testing.T) {
	data := map[string]any{"title": "no type field"}
	result := recordingTypeEndpoint(data, "999")
	assert.Equal(t, "", result, "missing type should return empty string")
}

func TestRecordingTypeEndpoint_EmptyType(t *testing.T) {
	data := map[string]any{"type": ""}
	result := recordingTypeEndpoint(data, "999")
	assert.Equal(t, "", result, "empty type should return empty string")
}
