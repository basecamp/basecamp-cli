package urlarg

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// A URL with segments past an item names the item only through a path bc3
// serves for it. Anything else is refused, never resolved to the parent.
func TestParseTrailingSegments(t *testing.T) {
	const base = "https://app.basecamp.com/99999/buckets/123"
	tests := []struct {
		name  string
		input string
		want  *Parsed
	}{
		{
			name:  "occurrence edit page keeps the occurrence date",
			input: base + "/schedule_entries/456/occurrences/20261022/edit",
			want:  &Parsed{AccountID: "99999", ProjectID: "123", Type: "schedule_entries", RecordingID: "456", OccurrenceDate: "20261022"},
		},
		{
			name:  "document in a folder names the document",
			input: base + "/vaults/111/documents/456",
			want:  &Parsed{AccountID: "99999", ProjectID: "123", Type: "documents", RecordingID: "456"},
		},
		{
			name:  "document edit page in a folder names the document",
			input: base + "/vaults/111/documents/456/edit",
			want:  &Parsed{AccountID: "99999", ProjectID: "123", Type: "documents", RecordingID: "456"},
		},
		{
			name:  "upload in a folder names the upload",
			input: base + "/vaults/111/uploads/456",
			want:  &Parsed{AccountID: "99999", ProjectID: "123", Type: "uploads", RecordingID: "456"},
		},
		{
			name:  "Google document in a folder names the Google document",
			input: base + "/vaults/111/google_documents/456",
			want:  &Parsed{AccountID: "99999", ProjectID: "123", Type: "google_documents", RecordingID: "456"},
		},
		{
			name:  "cloud file in a folder names the cloud file",
			input: base + "/vaults/111/cloud_files/456",
			want:  &Parsed{AccountID: "99999", ProjectID: "123", Type: "cloud_files", RecordingID: "456"},
		},
		{
			name:  "folder in a folder names the inner folder",
			input: base + "/vaults/111/vaults/222",
			want:  &Parsed{AccountID: "99999", ProjectID: "123", Type: "vaults", RecordingID: "222"},
		},
		{
			name:  "to-do in a to-do list names the to-do",
			input: base + "/todolists/111/todos/456",
			want:  &Parsed{AccountID: "99999", ProjectID: "123", Type: "todos", RecordingID: "456"},
		},
		{
			name:  "question in a questionnaire names the question",
			input: base + "/questionnaires/111/questions/456",
			want:  &Parsed{AccountID: "99999", ProjectID: "123", Type: "questions", RecordingID: "456"},
		},
		{
			name:  "chat line edit page names the line, not the chat",
			input: base + "/chats/789/lines/111/edit",
			want:  &Parsed{AccountID: "99999", ProjectID: "123", Type: "lines", RecordingID: "111", CampfireID: "789"},
		},
		{
			name:  "to-do edit page names the to-do",
			input: base + "/todos/789/edit",
			want:  &Parsed{AccountID: "99999", ProjectID: "123", Type: "todos", RecordingID: "789"},
		},
		{
			name:  "edit page with a trailing slash, query and comment fragment",
			input: base + "/todos/789/edit/?x=1#__recording_5",
			want:  &Parsed{AccountID: "99999", ProjectID: "123", Type: "todos", RecordingID: "789", CommentID: "5"},
		},
		{
			name:  "card edit page names the card",
			input: base + "/card_tables/cards/789/edit",
			want:  &Parsed{AccountID: "99999", ProjectID: "123", Type: "cards", RecordingID: "789"},
		},
		{
			name:  "upload download link names the upload",
			input: base + "/uploads/456/download/Q3%20report.pdf",
			want:  &Parsed{AccountID: "99999", ProjectID: "123", Type: "uploads", RecordingID: "456"},
		},
		{
			name:  "upload version download link names the upload",
			input: base + "/uploads/456/versions/7/download/a.pdf",
			want:  &Parsed{AccountID: "99999", ProjectID: "123", Type: "uploads", RecordingID: "456"},
		},
		{
			name:  "upload version download link with a query names the upload",
			input: base + "/uploads/456/versions/7/download/Q3%20report.pdf?disposition=attachment",
			want:  &Parsed{AccountID: "99999", ProjectID: "123", Type: "uploads", RecordingID: "456"},
		},
		{
			name:  "upload version preview names the upload",
			input: base + "/uploads/456/versions/7/representations/eyJfcmFpbHMiOnt9fQ==--a1b2",
			want:  &Parsed{AccountID: "99999", ProjectID: "123", Type: "uploads", RecordingID: "456"},
		},
		{
			name:  "new-version page names the upload",
			input: base + "/uploads/456/versions/new",
			want:  &Parsed{AccountID: "99999", ProjectID: "123", Type: "uploads", RecordingID: "456"},
		},
		{
			name:  "person edit page names the person",
			input: "https://app.basecamp.com/99999/people/5/edit",
			want:  &Parsed{AccountID: "99999", Type: "people", RecordingID: "5"},
		},
		{name: "unknown page under a to-do", input: base + "/todos/789/unknown"},
		{name: "occurrence status path", input: base + "/schedule_entries/456/occurrences/20261022/status/trashed"},
		{name: "new-document page in a folder", input: base + "/vaults/111/documents/new"},
		{name: "folder child the parser does not model", input: base + "/vaults/111/children/222"},
		{name: "answer under a question", input: base + "/questions/111/answers/2"},
		{name: "boost under a message", input: base + "/messages/789/boosts/5"},
		{name: "step under a card", input: base + "/card_tables/cards/789/steps/5"},
		{name: "upload version page bc3 does not serve", input: base + "/uploads/456/versions/7"},
		{name: "bucket without a resource", input: base},
		{name: "unknown page under a project", input: "https://app.basecamp.com/99999/projects/123/unknown"},
		{name: "unknown account-level page", input: "https://app.basecamp.com/99999/whatever/5/deep"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, Parse(tt.input))
		})
	}
}

// A refused URL is still a URL, so callers report it as one rather than
// treating it as a name or an ID.
func TestRefusedURLIsStillURL(t *testing.T) {
	url := "https://app.basecamp.com/99999/buckets/123/vaults/111/children/222"
	assert.True(t, IsURL(url))
	assert.Nil(t, Parse(url))
	assert.Equal(t, url, ExtractID(url))
}
