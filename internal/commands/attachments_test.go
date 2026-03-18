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

func TestAttachmentsURLWithoutComment(t *testing.T) {
	parsed := urlarg.Parse("https://3.basecamp.com/123/buckets/456/todos/111")
	assert.NotNil(t, parsed)
	assert.Equal(t, "", parsed.CommentID)
	assert.Equal(t, "111", parsed.RecordingID)
}
