package chrome

import (
	"fmt"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/tui"
	"github.com/basecamp/basecamp-cli/internal/tui/recents"
	"github.com/basecamp/basecamp-cli/internal/tui/workspace/data"
)

func testQuickJumpSource() QuickJumpSource {
	return QuickJumpSource{
		RecentProjects: []recents.Item{
			{ID: "100", Title: "Basecamp CLI", AccountID: "acct1"},
			{ID: "200", Title: "Hey Email", AccountID: "acct1"},
		},
		Projects: []data.ProjectInfo{
			{
				ID: 100, Name: "Basecamp CLI", AccountID: "acct1",
				Dock: []data.DockToolInfo{
					{ID: 1, Name: "todoset", Title: "Todos", Enabled: true},
					{ID: 2, Name: "message_board", Title: "Message Board", Enabled: true},
					{ID: 3, Name: "chat", Title: "Campfire", Enabled: true},
				},
			},
			{
				ID: 200, Name: "Hey Email", AccountID: "acct1",
				Dock: []data.DockToolInfo{
					{ID: 4, Name: "todoset", Title: "Todos", Enabled: true},
					{ID: 5, Name: "schedule", Title: "Schedule", Enabled: false},
				},
			},
		},
		AccountID: "acct1",
		NavigateProject: func(projectID int64, accountID string) tea.Cmd {
			return nil
		},
		NavigateRecording: func(recordingID, projectID int64, accountID string) tea.Cmd {
			return nil
		},
		NavigateTool: func(toolName string, toolID, projectID int64, accountID string) tea.Cmd {
			return nil
		},
	}
}

func TestQuickJump_ToolItems_Appear(t *testing.T) {
	styles := tui.NewStyles()
	qj := NewQuickJump(styles)
	qj.SetSize(80, 24)

	src := testQuickJumpSource()
	qj.Focus(src)

	// Find tool items
	var toolItems []quickJumpItem
	for _, item := range qj.items {
		if item.Category == "tool" {
			toolItems = append(toolItems, item)
		}
	}

	// Should have tool entries: Basecamp CLI > Todos, > Message Board, > Campfire
	// and Hey Email > Todos (schedule is disabled)
	require.Len(t, toolItems, 4, "expected 4 tool items (3 from CLI + 1 from Hey)")

	// Check titles
	titles := make([]string, len(toolItems))
	for i, item := range toolItems {
		titles[i] = item.Title
	}
	assert.Contains(t, titles, "Basecamp CLI > Todos")
	assert.Contains(t, titles, "Basecamp CLI > Message Board")
	assert.Contains(t, titles, "Basecamp CLI > Campfire")
	assert.Contains(t, titles, "Hey Email > Todos")
}

func TestQuickJump_ToolItems_DisabledToolsExcluded(t *testing.T) {
	styles := tui.NewStyles()
	qj := NewQuickJump(styles)
	qj.SetSize(80, 24)

	src := testQuickJumpSource()
	qj.Focus(src)

	for _, item := range qj.items {
		assert.NotContains(t, item.Title, "Schedule",
			"disabled tool (Schedule) should not appear")
	}
}

func TestQuickJump_ToolItem_FuzzyMatchesToolName(t *testing.T) {
	styles := tui.NewStyles()
	qj := NewQuickJump(styles)
	qj.SetSize(80, 24)

	src := testQuickJumpSource()
	qj.Focus(src)

	// Type "Todos" — should match tool items
	qj.input.SetValue("Todos")
	qj.refilter()

	var toolMatches int
	for _, item := range qj.filtered {
		if item.Category == "tool" {
			toolMatches++
		}
	}
	assert.GreaterOrEqual(t, toolMatches, 2, "fuzzy match on 'Todos' should find tool items")
}

func TestQuickJump_ToolItem_NavigatesToView(t *testing.T) {
	styles := tui.NewStyles()
	qj := NewQuickJump(styles)
	qj.SetSize(80, 24)

	var navigatedTool string
	var navigatedProjectID int64

	src := testQuickJumpSource()
	src.NavigateTool = func(toolName string, toolID, projectID int64, accountID string) tea.Cmd {
		navigatedTool = toolName
		navigatedProjectID = projectID
		return nil
	}
	qj.Focus(src)

	// Find the "Basecamp CLI > Todos" tool item and call its Navigate
	for _, item := range qj.items {
		if item.Title == "Basecamp CLI > Todos" {
			item.Navigate()
			break
		}
	}

	assert.Equal(t, "todoset", navigatedTool)
	assert.Equal(t, int64(100), navigatedProjectID)
}

func TestQuickJump_NoToolsWithoutCallback(t *testing.T) {
	styles := tui.NewStyles()
	qj := NewQuickJump(styles)
	qj.SetSize(80, 24)

	src := testQuickJumpSource()
	src.NavigateTool = nil // no callback
	qj.Focus(src)

	for _, item := range qj.items {
		assert.NotEqual(t, "tool", item.Category,
			"no tool items should appear without NavigateTool callback")
	}
}

func TestQuickJump_MaxFiveRecentProjects(t *testing.T) {
	styles := tui.NewStyles()
	qj := NewQuickJump(styles)
	qj.SetSize(80, 24)

	src := testQuickJumpSource()
	// Add 7 recent projects
	src.RecentProjects = nil
	src.Projects = nil
	for i := 1; i <= 7; i++ {
		id := int64(i * 100)
		src.RecentProjects = append(src.RecentProjects, recents.Item{
			ID:        fmt.Sprintf("%d", id),
			Title:     fmt.Sprintf("Project %d", i),
			AccountID: "acct1",
		})
		src.Projects = append(src.Projects, data.ProjectInfo{
			ID: id, Name: fmt.Sprintf("Project %d", i), AccountID: "acct1",
			Dock: []data.DockToolInfo{
				{ID: id + 1, Name: "todoset", Title: "Todos", Enabled: true},
			},
		})
	}

	qj.Focus(src)

	// Count unique project IDs in tool items
	projectIDs := make(map[string]bool)
	for _, item := range qj.items {
		if item.Category == "tool" {
			// Extract project name from "ProjectName > ToolName"
			projectIDs[item.Title] = true
		}
	}

	assert.LessOrEqual(t, len(projectIDs), 5, "tool items should come from at most 5 recent projects")
}
