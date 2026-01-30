package resolve

import (
	"context"
	"fmt"
	"strings"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"
	"github.com/charmbracelet/lipgloss"

	"github.com/basecamp/bcq/internal/output"
	"github.com/basecamp/bcq/internal/tui"
	"github.com/basecamp/bcq/internal/tui/format"
)

// CommentThread represents a thread of comments on a recording.
type CommentThread struct {
	Recording *basecamp.Recording
	Comments  []basecamp.Comment
	styles    *tui.Styles
}

// NewCommentThread creates a new comment thread view.
func NewCommentThread(recording *basecamp.Recording, comments []basecamp.Comment) *CommentThread {
	return &CommentThread{
		Recording: recording,
		Comments:  comments,
		styles:    tui.NewStyles(),
	}
}

// Render returns a formatted string representation of the comment thread.
func (ct *CommentThread) Render() string {
	var b strings.Builder

	// Recording header
	if ct.Recording != nil {
		b.WriteString(ct.renderRecordingHeader())
		b.WriteString("\n\n")
	}

	// Comments
	if len(ct.Comments) == 0 {
		b.WriteString(ct.styles.Muted.Render("No comments yet"))
	} else {
		for i, comment := range ct.Comments {
			b.WriteString(ct.renderComment(comment, i+1))
			if i < len(ct.Comments)-1 {
				b.WriteString("\n")
			}
		}
	}

	return b.String()
}

// renderRecordingHeader renders the recording header.
func (ct *CommentThread) renderRecordingHeader() string {
	rec := ct.Recording

	titleStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(ct.styles.Theme().Primary)

	typeStyle := ct.styles.Muted

	var header strings.Builder

	// Type and title
	typeName := format.RecordingTypeName(rec.Type)
	header.WriteString(typeStyle.Render(typeName) + " ")
	header.WriteString(titleStyle.Render(rec.Title))

	// ID and creator
	header.WriteString("\n")
	header.WriteString(ct.styles.Muted.Render(fmt.Sprintf("#%d", rec.ID)))
	if rec.Creator != nil {
		header.WriteString(ct.styles.Muted.Render(" by " + rec.Creator.Name))
	}

	return header.String()
}

// renderComment renders a single comment.
func (ct *CommentThread) renderComment(comment basecamp.Comment, _ int) string {
	var b strings.Builder

	// Comment header: author and time
	headerStyle := lipgloss.NewStyle().Bold(true)
	timeStyle := ct.styles.Muted

	author := "Unknown"
	if comment.Creator != nil {
		author = comment.Creator.Name
	}

	b.WriteString(headerStyle.Render(author))
	b.WriteString(" ")
	b.WriteString(timeStyle.Render(format.RelativeTime(comment.CreatedAt)))
	b.WriteString("\n")

	// Comment content (strip HTML for plain text display)
	content := stripCommentHTML(comment.Content)
	b.WriteString(content)
	b.WriteString("\n")

	return b.String()
}

// stripCommentHTML removes HTML tags from comment content for plain text display.
func stripCommentHTML(s string) string {
	var result strings.Builder
	inTag := false
	for _, c := range s {
		if c == '<' {
			inTag = true
			continue
		}
		if c == '>' {
			inTag = false
			result.WriteRune(' ') // Replace tag with space
			continue
		}
		if !inTag {
			result.WriteRune(c)
		}
	}
	// Normalize whitespace and trim
	return strings.TrimSpace(strings.Join(strings.Fields(result.String()), " "))
}

// FetchCommentThread fetches and returns a comment thread for a recording.
func FetchCommentThread(ctx context.Context, r *Resolver, projectID, recordingID int64) (*CommentThread, error) {
	if r.config.AccountID == "" {
		return nil, output.ErrUsage("Account must be resolved before fetching comments")
	}

	accountClient := r.sdk.ForAccount(r.config.AccountID)

	// Fetch the recording
	recording, err := accountClient.Recordings().Get(ctx, projectID, recordingID)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch recording: %w", err)
	}

	// Fetch comments
	commentsResult, err := accountClient.Comments().List(ctx, projectID, recordingID, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch comments: %w", err)
	}

	return NewCommentThread(recording, commentsResult.Comments), nil
}
