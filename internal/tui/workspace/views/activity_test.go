package views

import (
	"fmt"
	"strings"
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

// testActivitySession returns a minimal Session for activity view tests.
// Recents is nil (no cache dir), so the nil-check in openSelected is exercised.
func testActivitySession() *workspace.Session {
	return workspace.NewTestSession()
}

// testActivity creates an Activity view with pre-populated data for unit tests.
func testActivity(entries []data.TimelineEventInfo) *Activity {
	styles := tui.NewStyles()
	list := widget.NewList(styles)
	list.SetEmptyText("No recent activity.")
	list.SetFocused(true)
	list.SetSize(80, 20)

	pool := testPool("timeline", entries, true)

	v := &Activity{
		session:   testActivitySession(),
		pool:      pool,
		styles:    styles,
		list:      list,
		loading:   false,
		entryMeta: make(map[string]workspace.TimelineEventInfo),
	}

	v.syncEntries(entries)
	return v
}

func TestActivity_SyncEntries_KeysAreUnique(t *testing.T) {
	entries := sampleTimeline()
	v := testActivity(entries)

	// Every non-header list item should have a unique ID that maps to entryMeta.
	seen := make(map[string]bool)
	for _, item := range v.list.Items() {
		if item.Header {
			continue
		}
		assert.False(t, seen[item.ID], "list item ID %q must be unique", item.ID)
		seen[item.ID] = true
		assert.Contains(t, v.entryMeta, item.ID, "list item ID must exist in entryMeta")
	}
	assert.Equal(t, len(entries), len(seen), "must have one list item per entry")
}

func TestActivity_SyncEntries_MetaPreservesAccountContext(t *testing.T) {
	entries := sampleTimeline()
	v := testActivity(entries)

	// Each entry's metadata should carry the correct account and recording IDs.
	for _, meta := range v.entryMeta {
		assert.NotEmpty(t, meta.AccountID, "entryMeta must carry AccountID")
		assert.NotZero(t, meta.RecordingID, "entryMeta must carry RecordingID")
	}
}

func TestActivity_CrossAccountSameRecordingID_NoCollision(t *testing.T) {
	now := time.Now()
	entries := []data.TimelineEventInfo{
		{
			ID: 200, RecordingID: 9999,
			CreatedAt:   now.Add(-1 * time.Minute).Format("Jan 2 3:04pm"),
			CreatedAtTS: now.Add(-1 * time.Minute).Unix(),
			Action:      "completed", Target: "Todo", Title: "Same recording",
			Creator: "Alice", Project: "P1", ProjectID: 10,
			Account: "Acme", AccountID: "account-A",
		},
		{
			ID: 300, RecordingID: 9999, // same RecordingID, different account
			CreatedAt:   now.Add(-2 * time.Minute).Format("Jan 2 3:04pm"),
			CreatedAtTS: now.Add(-2 * time.Minute).Unix(),
			Action:      "created", Target: "Todo", Title: "Same recording other account",
			Creator: "Bob", Project: "P2", ProjectID: 20,
			Account: "Beta", AccountID: "account-B",
		},
	}

	v := testActivity(entries)

	// Both entries must be present — no collision.
	assert.Equal(t, 2, len(v.entryMeta), "both entries must survive (no key collision)")

	// Verify each entry retains its own account metadata.
	foundA, foundB := false, false
	for _, meta := range v.entryMeta {
		switch meta.AccountID {
		case "account-A":
			foundA = true
			assert.Equal(t, "Alice", meta.Creator)
		case "account-B":
			foundB = true
			assert.Equal(t, "Bob", meta.Creator)
		}
	}
	assert.True(t, foundA, "account-A entry must be present")
	assert.True(t, foundB, "account-B entry must be present")
}

func TestActivity_ZeroRecordingID_OpenSelectedReturnsStatus(t *testing.T) {
	now := time.Now()
	entries := []data.TimelineEventInfo{
		{
			ID: 500, RecordingID: 0, // no parent recording
			CreatedAt:   now.Add(-1 * time.Minute).Format("Jan 2 3:04pm"),
			CreatedAtTS: now.Add(-1 * time.Minute).Unix(),
			Action:      "archived", Target: "Project", Title: "Old project",
			Creator: "Alice", Project: "Archive", ProjectID: 0,
			Account: "Acme", AccountID: "a1",
		},
	}

	v := testActivity(entries)

	// Select the item and try to open it.
	cmd := v.openSelected()
	require.NotNil(t, cmd, "openSelected with RecordingID=0 should return a status cmd")

	// The cmd should produce a StatusMsg, not a NavigateMsg.
	msg := cmd()
	_, isNav := msg.(workspace.NavigateMsg)
	assert.False(t, isNav, "must not navigate when RecordingID is 0")

	// Verify it's a StatusMsg.
	status, isStatus := msg.(workspace.StatusMsg)
	assert.True(t, isStatus, "should produce StatusMsg for zero RecordingID")
	if isStatus {
		assert.True(t, strings.Contains(status.Text, "Cannot open"),
			"status text should explain why navigation was blocked")
	}
}

func TestActivity_ValidRecordingID_OpenSelectedNavigates(t *testing.T) {
	entries := sampleTimeline() // RecordingIDs 5001, 5002
	v := testActivity(entries)

	cmd := v.openSelected()
	// Session is nil so openSelected will panic if it tries recents.
	// But the first selected item has a valid RecordingID, so it should
	// attempt navigation. We can't fully test Navigate without a session,
	// but we CAN verify the cmd is non-nil (it would be nil if guarded out).
	//
	// With nil session, recents block is skipped (r == nil check).
	// Navigate returns a cmd that produces NavigateMsg.
	require.NotNil(t, cmd, "openSelected with valid RecordingID should return a cmd")

	msg := cmd()
	nav, isNav := msg.(workspace.NavigateMsg)
	require.True(t, isNav, "should produce NavigateMsg for valid RecordingID")
	assert.Equal(t, int64(5001), nav.Scope.RecordingID,
		"NavigateMsg must carry the RecordingID, not the event ID")
	assert.Equal(t, "a1", nav.Scope.AccountID,
		"NavigateMsg must carry the correct AccountID from the event metadata")
}

func TestActivity_SyncEntries_ListItemIDMatchesMetaKey(t *testing.T) {
	entries := sampleTimeline()
	v := testActivity(entries)

	// Every list item ID should have a corresponding entryMeta entry.
	for _, item := range v.list.Items() {
		if item.Header {
			continue
		}
		meta, ok := v.entryMeta[item.ID]
		require.True(t, ok, "list item %q must have entryMeta", item.ID)
		// The key should contain the event ID for uniqueness.
		assert.Contains(t, item.ID, fmt.Sprintf("%d", meta.ID),
			"list item key should contain the event ID")
	}
}
