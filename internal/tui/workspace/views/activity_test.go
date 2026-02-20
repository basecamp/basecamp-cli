package views

import (
	"fmt"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/tui"
	"github.com/basecamp/basecamp-cli/internal/tui/workspace"
	"github.com/basecamp/basecamp-cli/internal/tui/workspace/data"
	"github.com/basecamp/basecamp-cli/internal/tui/workspace/widget"
)

func sampleTimeline() []data.TimelineEventInfo {
	now := time.Now()
	return []data.TimelineEventInfo{
		{
			ID:          100,
			RecordingID: 5001,
			CreatedAt:   now.Add(-2 * time.Minute).Format("Jan 2 3:04pm"),
			CreatedAtTS: now.Add(-2 * time.Minute).Unix(),
			Action:      "completed",
			Target:      "Todo",
			Title:       "Ship feature",
			Creator:     "Alice",
			Project:     "Alpha",
			ProjectID:   42,
			Account:     "Acme",
			AccountID:   "a1",
		},
		{
			ID:          101,
			RecordingID: 5002,
			CreatedAt:   now.Add(-30 * time.Minute).Format("Jan 2 3:04pm"),
			CreatedAtTS: now.Add(-30 * time.Minute).Unix(),
			Action:      "created",
			Target:      "Message",
			Title:       "Weekly update",
			Creator:     "Bob",
			Project:     "Beta",
			ProjectID:   99,
			Account:     "Acme",
			AccountID:   "a1",
		},
	}
}

// testActivity creates an Activity view with pre-populated data for unit tests.
// Session is nil — tests that trigger navigation are not covered here.
func testActivity(entries []data.TimelineEventInfo) *Activity {
	styles := tui.NewStyles()
	list := widget.NewList(styles)
	list.SetEmptyText("No recent activity.")
	list.SetFocused(true)
	list.SetSize(80, 20)

	pool := testPool("timeline", entries, true)

	v := &Activity{
		pool:      pool,
		styles:    styles,
		list:      list,
		loading:   false,
		entryMeta: make(map[string]workspace.TimelineEventInfo),
	}

	v.syncEntries(entries)
	return v
}

func TestActivity_SyncEntries_UsesRecordingID(t *testing.T) {
	entries := sampleTimeline()
	v := testActivity(entries)

	// Every entry in entryMeta should be keyed by RecordingID (numeric string).
	for key, meta := range v.entryMeta {
		_, err := strconv.ParseInt(key, 10, 64)
		require.NoError(t, err, "entryMeta key %q must be a numeric string (RecordingID)", key)
		assert.Equal(t, fmt.Sprintf("%d", meta.RecordingID), key,
			"entryMeta key must match RecordingID")
	}

	// Specific check: event ID 100 has RecordingID 5001.
	assert.Contains(t, v.entryMeta, "5001", "must be keyed by RecordingID, not event ID")
	assert.NotContains(t, v.entryMeta, "100", "must not be keyed by event ID")
	assert.NotContains(t, v.entryMeta, "a1:100", "must not use composite key")
}

func TestActivity_SyncEntries_ListItemIDsAreRecordingIDs(t *testing.T) {
	entries := sampleTimeline()
	v := testActivity(entries)

	// Walk the list items — non-header items should have numeric IDs.
	for i, item := range v.list.Items() {
		if item.Header {
			continue
		}
		_, err := strconv.ParseInt(item.ID, 10, 64)
		assert.NoError(t, err, "list item ID %q at index %d must be a numeric RecordingID", item.ID, i)
	}
}
