package views

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/basecamp/basecamp-cli/internal/tui"
	"github.com/basecamp/basecamp-cli/internal/tui/recents"
	"github.com/basecamp/basecamp-cli/internal/tui/workspace"
	"github.com/basecamp/basecamp-cli/internal/tui/workspace/data"
	"github.com/basecamp/basecamp-cli/internal/tui/workspace/widget"
)

func TestAccountExtra_SingleAccount(t *testing.T) {
	accounts := []data.AccountInfo{{ID: "1", Name: "Acme"}}

	if got := accountExtra(accounts, "1", "Message"); got != "Message" {
		t.Errorf("single account: got %q, want %q", got, "Message")
	}
	if got := accountExtra(accounts, "1", ""); got != "" {
		t.Errorf("single account empty extra: got %q, want %q", got, "")
	}
}

func TestAccountExtra_MultiAccount(t *testing.T) {
	accounts := []data.AccountInfo{
		{ID: "aaa", Name: "Acme"},
		{ID: "bbb", Name: "Beta"},
		{ID: "ccc", Name: "Gamma"},
	}

	tests := []struct {
		name      string
		accountID string
		extra     string
		want      string
	}{
		{"first account with extra", "aaa", "Message", "1\u00b7Message"},
		{"second account with extra", "bbb", "Todo", "2\u00b7Todo"},
		{"third account with extra", "ccc", "Jan 15", "3\u00b7Jan 15"},
		{"empty extra preserved", "aaa", "", ""},
		{"unknown account", "zzz", "Message", "Message"},
		{"unknown account empty extra", "zzz", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := accountExtra(accounts, tt.accountID, tt.extra)
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestAccountExtra_NoAccounts(t *testing.T) {
	if got := accountExtra(nil, "1", "Message"); got != "Message" {
		t.Errorf("nil accounts: got %q, want %q", got, "Message")
	}
	if got := accountExtra([]data.AccountInfo{}, "1", "Message"); got != "Message" {
		t.Errorf("empty accounts: got %q, want %q", got, "Message")
	}
}

func TestAccountIndex(t *testing.T) {
	accounts := []data.AccountInfo{
		{ID: "aaa", Name: "Acme"},
		{ID: "bbb", Name: "Beta"},
	}

	if got := accountIndex(accounts, "aaa"); got != 1 {
		t.Errorf("first account: got %d, want 1", got)
	}
	if got := accountIndex(accounts, "bbb"); got != 2 {
		t.Errorf("second account: got %d, want 2", got)
	}
	if got := accountIndex(accounts, "zzz"); got != 0 {
		t.Errorf("unknown account: got %d, want 0", got)
	}
}

// --- View-level regression tests ---
// These exercise the full sync path with nil session (-> nil accounts -> no
// prefix) and confirm that empty Extra stays empty, preserving Description
// rendering in the list widget.

func TestActivity_EmptyExcerpt_PreservesDescription(t *testing.T) {
	now := time.Now()
	entries := []data.TimelineEventInfo{
		{
			RecordingID:    1001,
			CreatedAt:      now.Format("Jan 2 3:04pm"),
			CreatedAtTS:    now.Add(-5 * time.Minute).Unix(),
			Action:         "created",
			Target:         "Todo",
			Title:          "Ship it",
			Creator:        "Alice",
			Project:        "Alpha",
			SummaryExcerpt: "", // no excerpt
			AccountID:      "a1",
		},
	}
	v := testActivity(entries)

	for _, item := range v.list.Items() {
		if item.Header {
			continue
		}
		assert.Empty(t, item.Extra,
			"Activity item with no excerpt must have empty Extra to preserve Description")
		assert.NotEmpty(t, item.Description,
			"Activity item Description must remain visible when Extra is empty")
	}
}

func TestAssignments_NoDueDate_PreservesDescription(t *testing.T) {
	assignments := []data.AssignmentInfo{
		{
			ID:        2001,
			Content:   "Review design",
			Account:   "Acme",
			AccountID: "a1",
			Project:   "Alpha",
			ProjectID: 42,
			DueOn:     "", // no due date
		},
	}

	styles := tui.NewStyles()
	list := widget.NewList(styles)
	list.SetEmptyText("No assignments.")
	list.SetFocused(true)
	list.SetSize(80, 20)

	pool := testPool("assign", assignments, true)
	v := &Assignments{
		pool:           pool,
		styles:         styles,
		list:           list,
		assignmentMeta: make(map[string]workspace.AssignmentInfo),
	}
	v.syncAssignments(assignments)

	for _, item := range v.list.Items() {
		if item.Header {
			continue
		}
		assert.Empty(t, item.Extra,
			"Assignment with no DueOn must have empty Extra to preserve Description")
		assert.Contains(t, item.Description, "Acme",
			"Assignment Description must remain visible")
	}
}

func TestHome_Bookmarks_PreserveDescription(t *testing.T) {
	v := testHome(true)
	projects := []data.ProjectInfo{
		{
			ID:         100,
			Name:       "Alpha",
			Purpose:    "The main project",
			Bookmarked: true,
			AccountID:  "a1",
		},
	}
	v.syncBookmarks(projects)

	assert.NotEmpty(t, v.bookmarkItems, "should have bookmark items")
	for _, item := range v.bookmarkItems {
		assert.Empty(t, item.Extra,
			"Bookmark must have empty Extra to preserve Description (purpose)")
		assert.Equal(t, "The main project", item.Description,
			"Bookmark Description must show project purpose")
	}
}

// --- Search openSelected regression test ---
// Exercises the actual multi-account path: item.Extra carries a prefixed value
// like "2·Message", but openSelected() must read the raw type from resultMeta
// for RecordingType and recents.

func TestSearch_OpenSelected_UsesMeta_NotPrefixedExtra(t *testing.T) {
	store := recents.NewStore(t.TempDir())
	session := workspace.NewTestSessionWithRecents(store)
	styles := session.Styles()
	list := widget.NewList(styles)
	list.SetEmptyText("No results.")
	list.SetFocused(true)
	list.SetSize(80, 20)

	v := &Search{
		session:    session,
		styles:     styles,
		list:       list,
		query:      "test",
		resultMeta: make(map[string]workspace.SearchResultInfo),
	}

	// Simulate multi-account prefixed Extra (what handleResults would produce)
	id := fmt.Sprintf("%d", int64(5001))
	v.resultMeta[id] = workspace.SearchResultInfo{
		ID:        5001,
		Title:     "Weekly update",
		Type:      "Message",
		Project:   "Alpha",
		ProjectID: 42,
		Account:   "Beta Corp",
		AccountID: "acct-2",
	}
	list.SetItems([]widget.ListItem{
		{
			ID:          id,
			Title:       "Weekly update",
			Description: "Beta Corp > Alpha",
			Extra:       "2\u00b7Message", // prefixed — simulates multi-account
		},
	})

	cmd := v.openSelected()
	assert.NotNil(t, cmd, "openSelected must return a cmd")

	msg := cmd()
	nav, ok := msg.(workspace.NavigateMsg)
	assert.True(t, ok, "must produce NavigateMsg, got %T", msg)

	assert.Equal(t, "Message", nav.Scope.RecordingType,
		"RecordingType must come from resultMeta.Type, not the prefixed item.Extra")
	assert.Equal(t, int64(5001), nav.Scope.RecordingID,
		"RecordingID must come from resultMeta.ID")
	assert.Equal(t, "acct-2", nav.Scope.AccountID,
		"AccountID must come from resultMeta")
	assert.Equal(t, int64(42), nav.Scope.ProjectID,
		"ProjectID must come from resultMeta")

	// Verify recents entry uses raw type, not prefixed Extra
	saved := store.Get(recents.TypeRecording, "", "")
	assert.Len(t, saved, 1, "one recents entry should be saved")
	assert.Equal(t, "Message", saved[0].Description,
		"recents Description must be raw type from resultMeta, not prefixed Extra")
}
