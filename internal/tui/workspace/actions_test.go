package workspace

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewRegistry(t *testing.T) {
	r := NewRegistry()
	assert.Empty(t, r.All())
}

func TestRegistry_Register(t *testing.T) {
	r := NewRegistry()
	r.Register(Action{Name: ":test", Description: "test action"})

	all := r.All()
	require.Len(t, all, 1)
	assert.Equal(t, ":test", all[0].Name)
}

func TestRegistry_AllReturnsCopy(t *testing.T) {
	r := NewRegistry()
	r.Register(Action{Name: ":a"})

	all := r.All()
	all[0].Name = "mutated"
	assert.Equal(t, ":a", r.All()[0].Name, "mutation should not affect registry")
}

func TestRegistry_SearchEmptyQuery(t *testing.T) {
	r := NewRegistry()
	r.Register(Action{Name: ":a"})
	r.Register(Action{Name: ":b"})

	assert.Len(t, r.Search(""), 2, "empty query returns all")
}

func TestRegistry_SearchByName(t *testing.T) {
	r := NewRegistry()
	r.Register(Action{Name: ":todos", Description: "Navigate to todos"})
	r.Register(Action{Name: ":projects", Description: "Navigate to projects"})

	results := r.Search("todo")
	require.Len(t, results, 1)
	assert.Equal(t, ":todos", results[0].Name)
}

func TestRegistry_SearchByAlias(t *testing.T) {
	r := NewRegistry()
	r.Register(Action{Name: ":campfire", Aliases: []string{"chat", "fire"}})
	r.Register(Action{Name: ":todos"})

	results := r.Search("chat")
	require.Len(t, results, 1)
	assert.Equal(t, ":campfire", results[0].Name)
}

func TestRegistry_SearchByDescription(t *testing.T) {
	r := NewRegistry()
	r.Register(Action{Name: ":hey", Description: "Open Hey! inbox"})
	r.Register(Action{Name: ":quit", Description: "Quit bcq"})

	results := r.Search("inbox")
	require.Len(t, results, 1)
	assert.Equal(t, ":hey", results[0].Name)
}

func TestRegistry_SearchCaseInsensitive(t *testing.T) {
	r := NewRegistry()
	r.Register(Action{Name: ":Projects", Aliases: []string{"HOME"}})

	assert.Len(t, r.Search("project"), 1)
	assert.Len(t, r.Search("home"), 1)
	assert.Len(t, r.Search("HOME"), 1)
}

func TestRegistry_SearchNoMatch(t *testing.T) {
	r := NewRegistry()
	r.Register(Action{Name: ":todos"})

	assert.Empty(t, r.Search("zzz"))
}

func TestRegistry_ForScope_Any(t *testing.T) {
	r := NewRegistry()
	r.Register(Action{Name: ":a", Scope: ScopeAny})
	r.Register(Action{Name: ":b", Scope: ScopeProject})

	results := r.ForScope(Scope{})
	require.Len(t, results, 1)
	assert.Equal(t, ":a", results[0].Name)
}

func TestRegistry_ForScope_Account(t *testing.T) {
	r := NewRegistry()
	r.Register(Action{Name: ":a", Scope: ScopeAny})
	r.Register(Action{Name: ":b", Scope: ScopeAccount})
	r.Register(Action{Name: ":c", Scope: ScopeProject})

	results := r.ForScope(Scope{AccountID: "123"})
	require.Len(t, results, 2)
	assert.Equal(t, ":a", results[0].Name)
	assert.Equal(t, ":b", results[1].Name)
}

func TestRegistry_ForScope_Project(t *testing.T) {
	r := NewRegistry()
	r.Register(Action{Name: ":a", Scope: ScopeAny})
	r.Register(Action{Name: ":b", Scope: ScopeAccount})
	r.Register(Action{Name: ":c", Scope: ScopeProject})

	results := r.ForScope(Scope{AccountID: "123", ProjectID: 42})
	assert.Len(t, results, 3)
}

func TestDefaultActions(t *testing.T) {
	r := DefaultActions()
	all := r.All()

	// Verify expected count
	assert.GreaterOrEqual(t, len(all), 10)

	// Verify all have names and descriptions
	for _, a := range all {
		assert.NotEmpty(t, a.Name, "action should have a name")
		assert.NotEmpty(t, a.Description, "action %s should have a description", a.Name)
		assert.NotEmpty(t, a.Category, "action %s should have a category", a.Name)
		assert.NotNil(t, a.Execute, "action %s should have an Execute func", a.Name)
	}
}

func TestDefaultActions_QuitReturnsQuit(t *testing.T) {
	r := DefaultActions()
	results := r.Search("quit")
	require.Len(t, results, 1)

	cmd := results[0].Execute(nil)
	msg := cmd()
	_, isQuit := msg.(tea.QuitMsg)
	assert.True(t, isQuit, "quit action should produce tea.QuitMsg")
}

func TestDefaultActions_ProjectScopeActions(t *testing.T) {
	r := DefaultActions()

	// Without a project, these should be filtered out
	noProject := r.ForScope(Scope{AccountID: "1"})
	for _, a := range noProject {
		assert.NotEqual(t, ScopeProject, a.Scope,
			"action %s requires project but was returned for account-only scope", a.Name)
	}

	// With a project, they should appear
	withProject := r.ForScope(Scope{AccountID: "1", ProjectID: 42})
	names := make(map[string]bool)
	for _, a := range withProject {
		names[a.Name] = true
	}
	assert.True(t, names[":todos"])
	assert.True(t, names[":campfire"])
	assert.True(t, names[":messages"])
	assert.True(t, names[":cards"])
}

// -- :complete action tests --

func TestAction_Complete_AvailableForTodo(t *testing.T) {
	r := DefaultActions()
	scope := Scope{
		AccountID:     "1",
		ProjectID:     42,
		RecordingID:   100,
		RecordingType: "Todo",
	}
	names := actionNames(r.ForScope(scope))
	assert.True(t, names[":complete"], ":complete should be available for a Todo recording")
}

func TestAction_Complete_UnavailableForMessage(t *testing.T) {
	r := DefaultActions()
	scope := Scope{
		AccountID:     "1",
		ProjectID:     42,
		RecordingID:   100,
		RecordingType: "Message",
	}
	names := actionNames(r.ForScope(scope))
	assert.False(t, names[":complete"], ":complete should not be available for a Message")
}

func TestAction_Complete_UnavailableWithoutRecording(t *testing.T) {
	r := DefaultActions()
	scope := Scope{AccountID: "1", ProjectID: 42}
	names := actionNames(r.ForScope(scope))
	assert.False(t, names[":complete"], ":complete should not be available without RecordingID")
}

func TestAction_Complete_UnavailableWithoutProject(t *testing.T) {
	r := DefaultActions()
	scope := Scope{AccountID: "1", RecordingID: 100, RecordingType: "Todo"}
	names := actionNames(r.ForScope(scope))
	assert.False(t, names[":complete"], ":complete should not be available without ProjectID")
}

// -- :trash action tests --

func TestAction_Trash_AvailableForAnyRecording(t *testing.T) {
	r := DefaultActions()
	scope := Scope{
		AccountID:     "1",
		ProjectID:     42,
		RecordingID:   100,
		RecordingType: "Message",
	}
	names := actionNames(r.ForScope(scope))
	assert.True(t, names[":trash"], ":trash should be available for any recording")
}

func TestAction_Trash_UnavailableWithoutRecording(t *testing.T) {
	r := DefaultActions()
	scope := Scope{AccountID: "1", ProjectID: 42}
	names := actionNames(r.ForScope(scope))
	assert.False(t, names[":trash"], ":trash should not be available without RecordingID")
}

func TestAction_Available_RefinesScope_DoesNotBypass(t *testing.T) {
	r := NewRegistry()
	r.Register(Action{
		Name:  ":test",
		Scope: ScopeProject,
		Available: func(s Scope) bool {
			return true // always says yes
		},
		Execute: func(_ *Session) tea.Cmd { return nil },
	})

	// ProjectID=0 means scope check fails even though Available returns true
	results := r.ForScope(Scope{AccountID: "1"})
	assert.Empty(t, results, "Available should not bypass scope check")
}

func TestAction_Trash_ProducesStatusMsg_NotNavigateBack(t *testing.T) {
	r := DefaultActions()
	scope := Scope{
		AccountID:     "1",
		ProjectID:     42,
		RecordingID:   100,
		RecordingType: "Todo",
	}
	actions := r.ForScope(scope)
	var trashAction *Action
	for _, a := range actions {
		if a.Name == ":trash" {
			a := a
			trashAction = &a
			break
		}
	}
	require.NotNil(t, trashAction, ":trash should be in scope")

	// Execute with a test session that has Hub
	session := NewTestSessionWithScope(scope)
	cmd := trashAction.Execute(session)
	require.NotNil(t, cmd)

	msg := cmd()
	// Hub.TrashRecording will fail (nil SDK), so we get ErrorMsg
	_, isError := msg.(ErrorMsg)
	assert.True(t, isError, "palette trash should produce ErrorMsg (nil SDK), not NavigateBackMsg")
}

// -- helpers --

func actionNames(actions []Action) map[string]bool {
	m := make(map[string]bool, len(actions))
	for _, a := range actions {
		m[a.Name] = true
	}
	return m
}
