package resolve

import (
	"context"
	"fmt"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

	"github.com/basecamp/bcq/internal/output"
	"github.com/basecamp/bcq/internal/tui"
	"github.com/basecamp/bcq/internal/tui/format"
)

// CommentTarget holds the resolved target for a comment.
type CommentTarget struct {
	RecordingID int64
	ProjectID   int64
	Type        string
	Title       string
}

// Comment resolves the target recording for a comment using the following precedence:
// 1. CLI flag (--on)
// 2. Interactive prompt (if terminal is interactive)
// 3. Error (if no target can be determined)
//
// The project must be resolved before calling this method.
// Returns the resolved recording ID and project ID.
func (r *Resolver) Comment(ctx context.Context, onFlag string, projectID string) (*CommentTarget, error) {
	// 1. Check if recording ID is provided directly
	if onFlag != "" {
		return &CommentTarget{
			RecordingID: parseInt64(onFlag),
			ProjectID:   parseInt64(projectID),
		}, nil
	}

	// 2. Try interactive prompt if available
	if !r.IsInteractive() {
		return nil, output.ErrUsage("--on is required (recording ID to comment on)")
	}

	// Fetch recent recordings from the project to show as options
	recordings, err := r.fetchCommentableRecordings(ctx, projectID)
	if err != nil {
		return nil, err
	}

	if len(recordings) == 0 {
		return nil, output.ErrNotFound("recordings", projectID)
	}

	// Convert to picker items
	items := make([]tui.PickerItem, len(recordings))
	for i, rec := range recordings {
		formatted := format.Recording{
			ID:        rec.ID,
			Type:      rec.Type,
			Title:     rec.Title,
			CreatedAt: rec.CreatedAt,
		}
		if rec.Creator != nil {
			formatted.Creator = rec.Creator.Name
		}

		items[i] = tui.PickerItem{
			ID:          fmt.Sprintf("%d", rec.ID),
			Title:       formatted.ToPickerTitle(),
			Description: formatted.ToPickerDescription(),
		}
	}

	// Show picker
	selected, err := tui.NewPicker(items,
		tui.WithPickerTitle("Select item to comment on"),
		tui.WithEmptyMessage("No items found in this project"),
	).Run()

	if err != nil {
		return nil, fmt.Errorf("recording selection failed: %w", err)
	}
	if selected == nil {
		return nil, output.ErrUsage("recording selection canceled")
	}

	// Find the selected recording to get its type
	var selectedType string
	for _, rec := range recordings {
		if fmt.Sprintf("%d", rec.ID) == selected.ID {
			selectedType = rec.Type
			break
		}
	}

	return &CommentTarget{
		RecordingID: parseInt64(selected.ID),
		ProjectID:   parseInt64(projectID),
		Type:        selectedType,
		Title:       selected.Title,
	}, nil
}

// fetchCommentableRecordings retrieves recent recordings that can be commented on.
func (r *Resolver) fetchCommentableRecordings(ctx context.Context, projectID string) ([]basecamp.Recording, error) {
	if r.config.AccountID == "" {
		return nil, output.ErrUsage("Account must be resolved before fetching recordings")
	}

	bucketID := parseInt64(projectID)
	if bucketID == 0 {
		return nil, output.ErrUsage("Invalid project ID")
	}

	// Fetch different types of recordings that support comments
	var allRecordings []basecamp.Recording

	// Fetch todos
	todos, err := r.sdk.ForAccount(r.config.AccountID).Recordings().List(ctx, basecamp.RecordingTypeTodo, &basecamp.RecordingsListOptions{
		Bucket:    []int64{bucketID},
		Status:    "active",
		Sort:      "updated_at",
		Direction: "desc",
	})
	if err == nil {
		allRecordings = append(allRecordings, todos...)
	}

	// Fetch messages
	messages, err := r.sdk.ForAccount(r.config.AccountID).Recordings().List(ctx, basecamp.RecordingTypeMessage, &basecamp.RecordingsListOptions{
		Bucket:    []int64{bucketID},
		Status:    "active",
		Sort:      "updated_at",
		Direction: "desc",
	})
	if err == nil {
		allRecordings = append(allRecordings, messages...)
	}

	// Fetch documents
	docs, err := r.sdk.ForAccount(r.config.AccountID).Recordings().List(ctx, basecamp.RecordingTypeDocument, &basecamp.RecordingsListOptions{
		Bucket:    []int64{bucketID},
		Status:    "active",
		Sort:      "updated_at",
		Direction: "desc",
	})
	if err == nil {
		allRecordings = append(allRecordings, docs...)
	}

	// Limit to most recent items across all types
	maxItems := 50
	if len(allRecordings) > maxItems {
		allRecordings = allRecordings[:maxItems]
	}

	return allRecordings, nil
}

// parseInt64 safely parses a string to int64, returning 0 on error.
func parseInt64(s string) int64 {
	var result int64
	fmt.Sscanf(s, "%d", &result)
	return result
}
