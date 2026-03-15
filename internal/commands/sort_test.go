package commands

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

	"github.com/basecamp/basecamp-cli/internal/output"
)

func TestValidateSortField_Valid(t *testing.T) {
	err := validateSortField("title", []string{"title", "created", "updated"})
	assert.NoError(t, err)
}

func TestValidateSortField_Invalid(t *testing.T) {
	err := validateSortField("bogus", []string{"title", "created"})
	require.Error(t, err)

	var e *output.Error
	require.True(t, errors.As(err, &e))
	assert.Contains(t, e.Message, `"bogus"`)
	assert.Contains(t, e.Message, "title, created")
}

// --- sortTodos ---

func TestSortTodos_Title(t *testing.T) {
	todos := []basecamp.Todo{
		{Title: "Zebra"},
		{Title: "Apple"},
		{Title: "mango"},
	}
	sortTodos(todos, "title", false)
	assert.Equal(t, "Apple", todos[0].Title)
	assert.Equal(t, "mango", todos[1].Title)
	assert.Equal(t, "Zebra", todos[2].Title)
}

func TestSortTodos_TitleReversed(t *testing.T) {
	todos := []basecamp.Todo{
		{Title: "Apple"},
		{Title: "Zebra"},
	}
	sortTodos(todos, "title", true)
	assert.Equal(t, "Zebra", todos[0].Title)
	assert.Equal(t, "Apple", todos[1].Title)
}

func TestSortTodos_Created(t *testing.T) {
	t1 := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	t2 := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
	todos := []basecamp.Todo{
		{Title: "old", CreatedAt: t1},
		{Title: "new", CreatedAt: t2},
	}
	sortTodos(todos, "created", false)
	// Default: newest first
	assert.Equal(t, "new", todos[0].Title)
	assert.Equal(t, "old", todos[1].Title)
}

func TestSortTodos_CreatedReversed(t *testing.T) {
	t1 := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	t2 := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
	todos := []basecamp.Todo{
		{Title: "new", CreatedAt: t2},
		{Title: "old", CreatedAt: t1},
	}
	sortTodos(todos, "created", true)
	// Reversed: oldest first
	assert.Equal(t, "old", todos[0].Title)
	assert.Equal(t, "new", todos[1].Title)
}

func TestSortTodos_Updated(t *testing.T) {
	t1 := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	t2 := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
	todos := []basecamp.Todo{
		{Title: "stale", UpdatedAt: t1},
		{Title: "fresh", UpdatedAt: t2},
	}
	sortTodos(todos, "updated", false)
	assert.Equal(t, "fresh", todos[0].Title)
	assert.Equal(t, "stale", todos[1].Title)
}

func TestSortTodos_Position(t *testing.T) {
	todos := []basecamp.Todo{
		{Title: "third", Position: 3},
		{Title: "first", Position: 1},
		{Title: "second", Position: 2},
	}
	sortTodos(todos, "position", false)
	assert.Equal(t, "first", todos[0].Title)
	assert.Equal(t, "second", todos[1].Title)
	assert.Equal(t, "third", todos[2].Title)
}

func TestSortTodos_Due(t *testing.T) {
	todos := []basecamp.Todo{
		{Title: "no-due", DueOn: ""},
		{Title: "later", DueOn: "2025-12-31"},
		{Title: "soon", DueOn: "2025-01-15"},
	}
	sortTodos(todos, "due", false)
	// Default ascending: soonest first, empties last
	assert.Equal(t, "soon", todos[0].Title)
	assert.Equal(t, "later", todos[1].Title)
	assert.Equal(t, "no-due", todos[2].Title)
}

func TestSortTodos_DueReversed(t *testing.T) {
	todos := []basecamp.Todo{
		{Title: "no-due", DueOn: ""},
		{Title: "later", DueOn: "2025-12-31"},
		{Title: "soon", DueOn: "2025-01-15"},
	}
	sortTodos(todos, "due", true)
	// Reversed: empties first, then latest first
	assert.Equal(t, "no-due", todos[0].Title)
	assert.Equal(t, "later", todos[1].Title)
	assert.Equal(t, "soon", todos[2].Title)
}

// --- sortCards ---

func TestSortCards_Title(t *testing.T) {
	cards := []basecamp.Card{
		{Title: "Zebra"},
		{Title: "Apple"},
	}
	sortCards(cards, "title", false)
	assert.Equal(t, "Apple", cards[0].Title)
	assert.Equal(t, "Zebra", cards[1].Title)
}

func TestSortCards_Position(t *testing.T) {
	cards := []basecamp.Card{
		{Title: "b", Position: 2},
		{Title: "a", Position: 1},
	}
	sortCards(cards, "position", false)
	assert.Equal(t, "a", cards[0].Title)
	assert.Equal(t, "b", cards[1].Title)
}

func TestSortCards_Due(t *testing.T) {
	cards := []basecamp.Card{
		{Title: "none", DueOn: ""},
		{Title: "soon", DueOn: "2025-03-01"},
	}
	sortCards(cards, "due", false)
	assert.Equal(t, "soon", cards[0].Title)
	assert.Equal(t, "none", cards[1].Title)
}

func TestSortCards_TitleReversed(t *testing.T) {
	cards := []basecamp.Card{
		{Title: "Apple"},
		{Title: "Zebra"},
	}
	sortCards(cards, "title", true)
	assert.Equal(t, "Zebra", cards[0].Title)
	assert.Equal(t, "Apple", cards[1].Title)
}

func TestSortCards_Created(t *testing.T) {
	t1 := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	t2 := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
	cards := []basecamp.Card{
		{Title: "old", CreatedAt: t1},
		{Title: "new", CreatedAt: t2},
	}
	sortCards(cards, "created", false)
	assert.Equal(t, "new", cards[0].Title)
	assert.Equal(t, "old", cards[1].Title)
}

// --- sortMessages ---

func TestSortMessages_Title(t *testing.T) {
	msgs := []basecamp.Message{
		{Subject: "Zebra"},
		{Subject: "Apple"},
	}
	sortMessages(msgs, "title", false)
	assert.Equal(t, "Apple", msgs[0].Subject)
	assert.Equal(t, "Zebra", msgs[1].Subject)
}

func TestSortMessages_Created(t *testing.T) {
	t1 := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	t2 := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
	msgs := []basecamp.Message{
		{Subject: "old", CreatedAt: t1},
		{Subject: "new", CreatedAt: t2},
	}
	sortMessages(msgs, "created", false)
	assert.Equal(t, "new", msgs[0].Subject)
	assert.Equal(t, "old", msgs[1].Subject)
}

func TestSortMessages_TitleReversed(t *testing.T) {
	msgs := []basecamp.Message{
		{Subject: "Apple"},
		{Subject: "Zebra"},
	}
	sortMessages(msgs, "title", true)
	assert.Equal(t, "Zebra", msgs[0].Subject)
	assert.Equal(t, "Apple", msgs[1].Subject)
}

func TestSortMessages_Updated(t *testing.T) {
	t1 := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	t2 := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
	msgs := []basecamp.Message{
		{Subject: "stale", UpdatedAt: t1},
		{Subject: "fresh", UpdatedAt: t2},
	}
	sortMessages(msgs, "updated", false)
	assert.Equal(t, "fresh", msgs[0].Subject)
	assert.Equal(t, "stale", msgs[1].Subject)
}

// --- sortTodolists ---

func TestSortTodolists_Title(t *testing.T) {
	lists := []basecamp.Todolist{
		{Name: "Zebra"},
		{Name: "Apple"},
	}
	sortTodolists(lists, "title", false)
	assert.Equal(t, "Apple", lists[0].Name)
	assert.Equal(t, "Zebra", lists[1].Name)
}

func TestSortTodolists_Position(t *testing.T) {
	lists := []basecamp.Todolist{
		{Name: "b", Position: 2},
		{Name: "a", Position: 1},
	}
	sortTodolists(lists, "position", false)
	assert.Equal(t, "a", lists[0].Name)
	assert.Equal(t, "b", lists[1].Name)
}

func TestSortTodolists_TitleReversed(t *testing.T) {
	lists := []basecamp.Todolist{
		{Name: "Apple"},
		{Name: "Zebra"},
	}
	sortTodolists(lists, "title", true)
	assert.Equal(t, "Zebra", lists[0].Name)
	assert.Equal(t, "Apple", lists[1].Name)
}

func TestSortTodolists_Updated(t *testing.T) {
	t1 := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	t2 := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
	lists := []basecamp.Todolist{
		{Name: "stale", UpdatedAt: t1},
		{Name: "fresh", UpdatedAt: t2},
	}
	sortTodolists(lists, "updated", false)
	assert.Equal(t, "fresh", lists[0].Name)
	assert.Equal(t, "stale", lists[1].Name)
}

// --- sortProjects ---

func TestSortProjects_Title(t *testing.T) {
	projects := []basecamp.Project{
		{Name: "Zebra"},
		{Name: "Apple"},
	}
	sortProjects(projects, "title", false)
	assert.Equal(t, "Apple", projects[0].Name)
	assert.Equal(t, "Zebra", projects[1].Name)
}

func TestSortProjects_Created(t *testing.T) {
	t1 := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	t2 := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
	projects := []basecamp.Project{
		{Name: "old", CreatedAt: t1},
		{Name: "new", CreatedAt: t2},
	}
	sortProjects(projects, "created", false)
	assert.Equal(t, "new", projects[0].Name)
	assert.Equal(t, "old", projects[1].Name)
}

func TestSortProjects_Reversed(t *testing.T) {
	projects := []basecamp.Project{
		{Name: "Apple"},
		{Name: "Zebra"},
	}
	sortProjects(projects, "title", true)
	assert.Equal(t, "Zebra", projects[0].Name)
	assert.Equal(t, "Apple", projects[1].Name)
}

func TestSortProjects_Updated(t *testing.T) {
	t1 := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	t2 := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
	projects := []basecamp.Project{
		{Name: "stale", UpdatedAt: t1},
		{Name: "fresh", UpdatedAt: t2},
	}
	sortProjects(projects, "updated", false)
	assert.Equal(t, "fresh", projects[0].Name)
	assert.Equal(t, "stale", projects[1].Name)
}

// --- sortPeople ---

func TestSortPeople_Name(t *testing.T) {
	people := []basecamp.Person{
		{Name: "Zara"},
		{Name: "Alice"},
	}
	sortPeople(people, "name", false)
	assert.Equal(t, "Alice", people[0].Name)
	assert.Equal(t, "Zara", people[1].Name)
}

func TestSortPeople_NameReversed(t *testing.T) {
	people := []basecamp.Person{
		{Name: "Alice"},
		{Name: "Zara"},
	}
	sortPeople(people, "name", true)
	assert.Equal(t, "Zara", people[0].Name)
	assert.Equal(t, "Alice", people[1].Name)
}

// --- sortScheduleEntries ---

func TestSortScheduleEntries_Title(t *testing.T) {
	entries := []basecamp.ScheduleEntry{
		{Title: "Zebra"},
		{Title: "Apple"},
	}
	sortScheduleEntries(entries, "title", false)
	assert.Equal(t, "Apple", entries[0].Title)
	assert.Equal(t, "Zebra", entries[1].Title)
}

func TestSortScheduleEntries_TitlePrefersSummary(t *testing.T) {
	entries := []basecamp.ScheduleEntry{
		{Summary: "Zebra", Title: "aaa"},
		{Summary: "Apple", Title: "zzz"},
	}
	sortScheduleEntries(entries, "title", false)
	// Should sort by Summary, not Title
	assert.Equal(t, "Apple", entries[0].Summary)
	assert.Equal(t, "Zebra", entries[1].Summary)
}

func TestSortScheduleEntries_TitleFallsBackToTitle(t *testing.T) {
	entries := []basecamp.ScheduleEntry{
		{Summary: "", Title: "Zebra"},
		{Summary: "Apple", Title: ""},
	}
	sortScheduleEntries(entries, "title", false)
	assert.Equal(t, "Apple", entries[0].Summary)
	assert.Equal(t, "", entries[1].Summary)
}

func TestSortScheduleEntries_Created(t *testing.T) {
	t1 := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	t2 := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
	entries := []basecamp.ScheduleEntry{
		{Title: "old", CreatedAt: t1},
		{Title: "new", CreatedAt: t2},
	}
	sortScheduleEntries(entries, "created", false)
	assert.Equal(t, "new", entries[0].Title)
	assert.Equal(t, "old", entries[1].Title)
}

func TestSortScheduleEntries_TitleReversed(t *testing.T) {
	entries := []basecamp.ScheduleEntry{
		{Title: "Apple"},
		{Title: "Zebra"},
	}
	sortScheduleEntries(entries, "title", true)
	assert.Equal(t, "Zebra", entries[0].Title)
	assert.Equal(t, "Apple", entries[1].Title)
}

func TestSortScheduleEntries_Updated(t *testing.T) {
	t1 := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	t2 := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
	entries := []basecamp.ScheduleEntry{
		{Title: "stale", UpdatedAt: t1},
		{Title: "fresh", UpdatedAt: t2},
	}
	sortScheduleEntries(entries, "updated", false)
	assert.Equal(t, "fresh", entries[0].Title)
	assert.Equal(t, "stale", entries[1].Title)
}

// --- compareDueOn edge cases ---

func TestCompareDueOn_BothEmpty(t *testing.T) {
	assert.False(t, compareDueOn("", ""))
}

func TestCompareDueOn_FirstEmpty(t *testing.T) {
	assert.False(t, compareDueOn("", "2025-01-01"))
}

func TestCompareDueOn_SecondEmpty(t *testing.T) {
	assert.True(t, compareDueOn("2025-01-01", ""))
}

func TestCompareDueOn_BothPresent(t *testing.T) {
	assert.True(t, compareDueOn("2025-01-01", "2025-12-31"))
	assert.False(t, compareDueOn("2025-12-31", "2025-01-01"))
}

// --- Validation logic tests (command-level guards) ---

func TestValidateSortField_PositionNotInAggregateSet(t *testing.T) {
	// position is only valid in single-parent contexts (single column/todolist),
	// not when aggregating across columns or todolists
	err := validateSortField("position", []string{"title", "created", "updated", "due"})
	require.Error(t, err)

	var e *output.Error
	require.True(t, errors.As(err, &e))
	assert.Contains(t, e.Message, `"position"`)
	assert.Contains(t, e.Message, "title, created, updated, due")
}

func TestValidateSortField_PositionAllowedInSingleParent(t *testing.T) {
	err := validateSortField("position", []string{"title", "created", "updated", "position", "due"})
	assert.NoError(t, err)
}
