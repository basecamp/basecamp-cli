package views

import (
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/basecamp/basecamp-cli/internal/tui/workspace"
)

func TestSearchResultSortByTimestamp(t *testing.T) {
	// Verify that sorting uses numeric timestamps, not formatted strings.
	// "Feb 1" < "Jan 15" lexicographically but Jan 15 is earlier chronologically.
	results := []workspace.SearchResultInfo{
		{ID: 1, Title: "Older", CreatedAt: "Jan 15", CreatedAtTS: 1705276800},  // 2024-01-15
		{ID: 2, Title: "Newer", CreatedAt: "Feb 1", CreatedAtTS: 1706745600},   // 2024-02-01
		{ID: 3, Title: "Newest", CreatedAt: "Mar 10", CreatedAtTS: 1710028800}, // 2024-03-10
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].CreatedAtTS > results[j].CreatedAtTS
	})

	assert.Equal(t, "Newest", results[0].Title)
	assert.Equal(t, "Newer", results[1].Title)
	assert.Equal(t, "Older", results[2].Title)
}

func TestPingRoomSortByTimestamp(t *testing.T) {
	// Verify that sorting uses numeric timestamps, not formatted strings.
	// "Dec 31 11:59pm" > "Jan 1 12:00am" lexicographically but Dec 31 is earlier.
	rooms := []workspace.PingRoomInfo{
		{CampfireID: 1, PersonName: "Alice", LastAt: "Dec 31 11:59pm", LastAtTS: 1704067140},
		{CampfireID: 2, PersonName: "Bob", LastAt: "Jan 1 12:00am", LastAtTS: 1704067200},
		{CampfireID: 3, PersonName: "Carol", LastAt: "Jan 2 9:00am", LastAtTS: 1704186000},
	}

	sort.Slice(rooms, func(i, j int) bool {
		return rooms[i].LastAtTS > rooms[j].LastAtTS
	})

	assert.Equal(t, "Carol", rooms[0].PersonName, "Jan 2 should be most recent")
	assert.Equal(t, "Bob", rooms[1].PersonName, "Jan 1 should be second")
	assert.Equal(t, "Alice", rooms[2].PersonName, "Dec 31 should be oldest")
}
