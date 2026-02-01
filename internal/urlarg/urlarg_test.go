package urlarg

import (
	"testing"
)

func TestIsURL(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"https://3.basecamp.com/123/buckets/456/todos/789", true},
		{"https://3.basecamp.com/123/projects/456", true},
		{"http://localhost:3000/123/buckets/456/todos/789", true},
		{"123", false},
		{"my-project", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			if got := IsURL(tt.input); got != tt.want {
				t.Errorf("IsURL(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestParse(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    *Parsed
		wantNil bool
	}{
		{
			name:  "full todo URL",
			input: "https://3.basecamp.com/123/buckets/456/todos/789",
			want: &Parsed{
				AccountID:   "123",
				ProjectID:   "456",
				Type:        "todos",
				RecordingID: "789",
			},
		},
		{
			name:  "message URL",
			input: "https://3.basecamp.com/123/buckets/456/messages/789",
			want: &Parsed{
				AccountID:   "123",
				ProjectID:   "456",
				Type:        "messages",
				RecordingID: "789",
			},
		},
		{
			name:  "URL with comment fragment",
			input: "https://3.basecamp.com/123/buckets/456/todos/789#__recording_999",
			want: &Parsed{
				AccountID:   "123",
				ProjectID:   "456",
				Type:        "todos",
				RecordingID: "789",
				CommentID:   "999",
			},
		},
		{
			name:  "card URL",
			input: "https://3.basecamp.com/123/buckets/456/card_tables/cards/789",
			want: &Parsed{
				AccountID:   "123",
				ProjectID:   "456",
				Type:        "cards",
				RecordingID: "789",
			},
		},
		{
			name:  "column URL",
			input: "https://3.basecamp.com/123/buckets/456/card_tables/columns/789",
			want: &Parsed{
				AccountID:   "123",
				ProjectID:   "456",
				Type:        "columns",
				RecordingID: "789",
			},
		},
		{
			name:  "project URL",
			input: "https://3.basecamp.com/123/projects/456",
			want: &Parsed{
				AccountID: "123",
				ProjectID: "456",
				Type:      "project",
			},
		},
		{
			name:  "type list URL (todolists)",
			input: "https://3.basecamp.com/123/buckets/456/todolists",
			want: &Parsed{
				AccountID: "123",
				ProjectID: "456",
				Type:      "todolists",
			},
		},
		{
			name:    "plain ID",
			input:   "789",
			wantNil: true,
		},
		{
			name:    "project name",
			input:   "my-project",
			wantNil: true,
		},
		{
			name:    "empty string",
			input:   "",
			wantNil: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Parse(tt.input)
			if tt.wantNil {
				if got != nil {
					t.Errorf("Parse(%q) = %+v, want nil", tt.input, got)
				}
				return
			}
			if got == nil {
				t.Errorf("Parse(%q) = nil, want %+v", tt.input, tt.want)
				return
			}
			if got.AccountID != tt.want.AccountID {
				t.Errorf("AccountID = %q, want %q", got.AccountID, tt.want.AccountID)
			}
			if got.ProjectID != tt.want.ProjectID {
				t.Errorf("ProjectID = %q, want %q", got.ProjectID, tt.want.ProjectID)
			}
			if got.Type != tt.want.Type {
				t.Errorf("Type = %q, want %q", got.Type, tt.want.Type)
			}
			if got.RecordingID != tt.want.RecordingID {
				t.Errorf("RecordingID = %q, want %q", got.RecordingID, tt.want.RecordingID)
			}
			if got.CommentID != tt.want.CommentID {
				t.Errorf("CommentID = %q, want %q", got.CommentID, tt.want.CommentID)
			}
		})
	}
}

func TestExtractID(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"https://3.basecamp.com/123/buckets/456/todos/789", "789"},
		{"https://3.basecamp.com/123/projects/456", "456"},
		{"https://3.basecamp.com/123/buckets/456/card_tables/cards/789", "789"},
		{"789", "789"},
		{"my-project", "my-project"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			if got := ExtractID(tt.input); got != tt.want {
				t.Errorf("ExtractID(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestExtractProjectID(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"https://3.basecamp.com/123/buckets/456/todos/789", "456"},
		{"https://3.basecamp.com/123/projects/456", "456"},
		{"456", "456"},
		{"my-project", "my-project"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			if got := ExtractProjectID(tt.input); got != tt.want {
				t.Errorf("ExtractProjectID(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestExtractWithProject(t *testing.T) {
	tests := []struct {
		input           string
		wantRecordingID string
		wantProjectID   string
	}{
		{"https://3.basecamp.com/123/buckets/456/todos/789", "789", "456"},
		{"https://3.basecamp.com/123/projects/456", "", "456"}, // project URL returns empty recordingID
		{"789", "789", ""},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			gotRecordingID, gotProjectID := ExtractWithProject(tt.input)
			if gotRecordingID != tt.wantRecordingID {
				t.Errorf("recordingID = %q, want %q", gotRecordingID, tt.wantRecordingID)
			}
			if gotProjectID != tt.wantProjectID {
				t.Errorf("projectID = %q, want %q", gotProjectID, tt.wantProjectID)
			}
		})
	}
}

func TestExtractCommentWithProject(t *testing.T) {
	tests := []struct {
		input    string
		wantID   string
		wantProj string
	}{
		// Comment URL with fragment — should extract comment ID, not recording ID
		{"https://3.basecamp.com/123/buckets/456/todos/111#__recording_789", "789", "456"},
		// Regular URL without fragment — should extract recording ID
		{"https://3.basecamp.com/123/buckets/456/todos/789", "789", "456"},
		// Plain ID
		{"789", "789", ""},
		// Numeric fragment
		{"https://3.basecamp.com/123/buckets/456/messages/111#999", "999", "456"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			gotID, gotProj := ExtractCommentWithProject(tt.input)
			if gotID != tt.wantID {
				t.Errorf("id = %q, want %q", gotID, tt.wantID)
			}
			if gotProj != tt.wantProj {
				t.Errorf("projectID = %q, want %q", gotProj, tt.wantProj)
			}
		})
	}
}

func TestExtractIDs(t *testing.T) {
	args := []string{
		"https://3.basecamp.com/123/buckets/456/todos/789",
		"111",
		"https://3.basecamp.com/123/buckets/456/messages/222",
	}
	want := []string{"789", "111", "222"}
	got := ExtractIDs(args)

	if len(got) != len(want) {
		t.Errorf("ExtractIDs() = %v, want %v", got, want)
		return
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("ExtractIDs()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}
