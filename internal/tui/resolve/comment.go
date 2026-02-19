package resolve

import (
	"context"
	"fmt"
	"sort"
	"strconv"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

	"github.com/basecamp/basecamp-cli/internal/output"
	"github.com/basecamp/basecamp-cli/internal/tui"
	"github.com/basecamp/basecamp-cli/internal/tui/format"
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
	// Validate project ID is numeric
	parsedProjectID, err := parseRequiredInt64(projectID, "project ID")
	if err != nil {
		return nil, err
	}

	// 1. Check if recording ID is provided directly
	if onFlag != "" {
		recordingID, err := parseRequiredInt64(onFlag, "recording ID (--on)")
		if err != nil {
			return nil, err
		}
		return &CommentTarget{
			RecordingID: recordingID,
			ProjectID:   parsedProjectID,
		}, nil
	}

	// 2. Try interactive prompt if available
	if !r.IsInteractive() {
		return nil, output.ErrUsage("--on is required (recording ID to comment on)")
	}

	// Fetch recent recordings from the project to show as options
	recordings, err := r.fetchCommentableRecordings(ctx, parsedProjectID)
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

	// Parse selected recording ID (should always succeed since we created the picker items)
	selectedRecordingID, _ := strconv.ParseInt(selected.ID, 10, 64)

	return &CommentTarget{
		RecordingID: selectedRecordingID,
		ProjectID:   parsedProjectID,
		Type:        selectedType,
		Title:       selected.Title,
	}, nil
}

// fetchCommentableRecordings retrieves recent recordings that can be commented on.
func (r *Resolver) fetchCommentableRecordings(ctx context.Context, bucketID int64) ([]basecamp.Recording, error) {
	if r.config.AccountID == "" {
		return nil, output.ErrUsage("Account must be resolved before fetching recordings")
	}

	// Fetch different types of recordings that support comments
	var allRecordings []basecamp.Recording
	var errs []error

	// Fetch todos
	todosResult, err := r.sdk.ForAccount(r.config.AccountID).Recordings().List(ctx, basecamp.RecordingTypeTodo, &basecamp.RecordingsListOptions{
		Bucket:    []int64{bucketID},
		Status:    "active",
		Sort:      "updated_at",
		Direction: "desc",
	})
	if err != nil {
		errs = append(errs, fmt.Errorf("todos: %w", err))
	} else {
		allRecordings = append(allRecordings, todosResult.Recordings...)
	}

	// Fetch messages
	messagesResult, err := r.sdk.ForAccount(r.config.AccountID).Recordings().List(ctx, basecamp.RecordingTypeMessage, &basecamp.RecordingsListOptions{
		Bucket:    []int64{bucketID},
		Status:    "active",
		Sort:      "updated_at",
		Direction: "desc",
	})
	if err != nil {
		errs = append(errs, fmt.Errorf("messages: %w", err))
	} else {
		allRecordings = append(allRecordings, messagesResult.Recordings...)
	}

	// Fetch documents
	docsResult, err := r.sdk.ForAccount(r.config.AccountID).Recordings().List(ctx, basecamp.RecordingTypeDocument, &basecamp.RecordingsListOptions{
		Bucket:    []int64{bucketID},
		Status:    "active",
		Sort:      "updated_at",
		Direction: "desc",
	})
	if err != nil {
		errs = append(errs, fmt.Errorf("documents: %w", err))
	} else {
		allRecordings = append(allRecordings, docsResult.Recordings...)
	}

	// If all fetches failed, return the combined error
	if len(errs) > 0 && len(allRecordings) == 0 {
		return nil, fmt.Errorf("failed to fetch recordings: %v", errs)
	}

	// Sort by UpdatedAt (descending) to get truly most recent across all types
	sort.Slice(allRecordings, func(i, j int) bool {
		return allRecordings[i].UpdatedAt.After(allRecordings[j].UpdatedAt)
	})

	// Limit to most recent items
	maxItems := 50
	if len(allRecordings) > maxItems {
		allRecordings = allRecordings[:maxItems]
	}

	return allRecordings, nil
}

// parseRequiredInt64 parses a string to int64 and returns a usage error if invalid.
func parseRequiredInt64(s string, name string) (int64, error) {
	if s == "" {
		return 0, output.ErrUsage(name + " is required")
	}
	result, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, output.ErrUsage(fmt.Sprintf("Invalid %s %q: must be a numeric ID", name, s))
	}
	if result <= 0 {
		return 0, output.ErrUsage(fmt.Sprintf("Invalid %s %q: must be a positive number", name, s))
	}
	return result, nil
}
