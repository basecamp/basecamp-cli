package workspace

import (
	"testing"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
)

// mockView satisfies the View interface for testing.
type mockView struct {
	title string
}

func (v mockView) Init() tea.Cmd                       { return nil }
func (v mockView) Update(tea.Msg) (tea.Model, tea.Cmd) { return v, nil }
func (v mockView) View() string                        { return v.title }
func (v mockView) Title() string                       { return v.title }
func (v mockView) ShortHelp() []key.Binding            { return nil }
func (v mockView) FullHelp() [][]key.Binding           { return nil }
func (v mockView) SetSize(int, int)                    {}

func TestNewRouter(t *testing.T) {
	r := NewRouter()

	assert.Equal(t, 0, r.Depth())
	assert.False(t, r.CanGoBack())
	assert.Nil(t, r.Current())
	assert.Empty(t, r.Breadcrumbs())
}

func TestRouter_Push(t *testing.T) {
	r := NewRouter()

	r.Push(mockView{title: "Home"}, Scope{AccountID: "1"})
	assert.Equal(t, 1, r.Depth())
	assert.Equal(t, "Home", r.Current().(mockView).title)
	assert.False(t, r.CanGoBack(), "single entry cannot go back")

	r.Push(mockView{title: "Project"}, Scope{AccountID: "1", ProjectID: 42})
	assert.Equal(t, 2, r.Depth())
	assert.Equal(t, "Project", r.Current().(mockView).title)
	assert.True(t, r.CanGoBack())
}

func TestRouter_Pop(t *testing.T) {
	r := NewRouter()

	r.Push(mockView{title: "Home"}, Scope{})
	r.Push(mockView{title: "Project"}, Scope{ProjectID: 1})
	r.Push(mockView{title: "Todolist"}, Scope{ProjectID: 1, ToolID: 2})

	assert.Equal(t, 3, r.Depth())

	// Pop returns the new current view
	v := r.Pop()
	assert.Equal(t, "Project", v.(mockView).title)
	assert.Equal(t, 2, r.Depth())

	v = r.Pop()
	assert.Equal(t, "Home", v.(mockView).title)
	assert.Equal(t, 1, r.Depth())
}

func TestRouter_PopProtectsRoot(t *testing.T) {
	r := NewRouter()
	r.Push(mockView{title: "Root"}, Scope{})

	v := r.Pop()
	assert.Nil(t, v, "Pop on root should return nil")
	assert.Equal(t, 1, r.Depth(), "root should remain on the stack")
	assert.Equal(t, "Root", r.Current().(mockView).title)
}

func TestRouter_PopEmptyStack(t *testing.T) {
	r := NewRouter()

	v := r.Pop()
	assert.Nil(t, v)
	assert.Equal(t, 0, r.Depth())
}

func TestRouter_CurrentScope(t *testing.T) {
	r := NewRouter()

	// Empty router returns zero Scope
	assert.Equal(t, Scope{}, r.CurrentScope())

	scope1 := Scope{AccountID: "acct-1"}
	r.Push(mockView{title: "Home"}, scope1)
	assert.Equal(t, scope1, r.CurrentScope())

	scope2 := Scope{AccountID: "acct-1", ProjectID: 99, ProjectName: "HQ"}
	r.Push(mockView{title: "Project"}, scope2)
	assert.Equal(t, scope2, r.CurrentScope())

	r.Pop()
	assert.Equal(t, scope1, r.CurrentScope(), "scope should revert after pop")
}

func TestRouter_Breadcrumbs(t *testing.T) {
	r := NewRouter()

	r.Push(mockView{title: "Home"}, Scope{})
	r.Push(mockView{title: "HQ"}, Scope{})
	r.Push(mockView{title: "To-dos"}, Scope{})

	crumbs := r.Breadcrumbs()
	assert.Equal(t, []string{"Home", "HQ", "To-dos"}, crumbs)
}

func TestRouter_BreadcrumbsReflectPop(t *testing.T) {
	r := NewRouter()

	r.Push(mockView{title: "A"}, Scope{})
	r.Push(mockView{title: "B"}, Scope{})
	r.Push(mockView{title: "C"}, Scope{})

	r.Pop()
	assert.Equal(t, []string{"A", "B"}, r.Breadcrumbs())

	r.Pop()
	assert.Equal(t, []string{"A"}, r.Breadcrumbs())
}

func TestRouter_PopToDepth(t *testing.T) {
	r := NewRouter()

	r.Push(mockView{title: "Root"}, Scope{})
	r.Push(mockView{title: "Level 2"}, Scope{})
	r.Push(mockView{title: "Level 3"}, Scope{})
	r.Push(mockView{title: "Level 4"}, Scope{})

	assert.Equal(t, 4, r.Depth())

	// Pop back to depth 2
	v := r.PopToDepth(2)
	assert.Equal(t, "Level 2", v.(mockView).title)
	assert.Equal(t, 2, r.Depth())
	assert.Equal(t, []string{"Root", "Level 2"}, r.Breadcrumbs())
}

func TestRouter_PopToDepthOne(t *testing.T) {
	r := NewRouter()

	r.Push(mockView{title: "Root"}, Scope{})
	r.Push(mockView{title: "Deep"}, Scope{})
	r.Push(mockView{title: "Deeper"}, Scope{})

	v := r.PopToDepth(1)
	assert.Equal(t, "Root", v.(mockView).title)
	assert.Equal(t, 1, r.Depth())
}

func TestRouter_PopToDepthInvalid(t *testing.T) {
	r := NewRouter()

	r.Push(mockView{title: "Root"}, Scope{})
	r.Push(mockView{title: "Child"}, Scope{})

	// Zero is invalid
	assert.Nil(t, r.PopToDepth(0))
	assert.Equal(t, 2, r.Depth(), "stack unchanged on invalid depth")

	// Negative is invalid
	assert.Nil(t, r.PopToDepth(-1))
	assert.Equal(t, 2, r.Depth())

	// Beyond current depth is invalid
	assert.Nil(t, r.PopToDepth(5))
	assert.Equal(t, 2, r.Depth())
}

func TestRouter_PopToDepthSameDepth(t *testing.T) {
	r := NewRouter()

	r.Push(mockView{title: "Root"}, Scope{})
	r.Push(mockView{title: "Current"}, Scope{})

	// Pop to the current depth is a no-op
	v := r.PopToDepth(2)
	assert.Equal(t, "Current", v.(mockView).title)
	assert.Equal(t, 2, r.Depth())
}

func TestRouter_CanGoBack(t *testing.T) {
	r := NewRouter()

	assert.False(t, r.CanGoBack(), "empty stack")

	r.Push(mockView{title: "One"}, Scope{})
	assert.False(t, r.CanGoBack(), "single entry")

	r.Push(mockView{title: "Two"}, Scope{})
	assert.True(t, r.CanGoBack(), "two entries")

	r.Push(mockView{title: "Three"}, Scope{})
	assert.True(t, r.CanGoBack(), "three entries")

	r.Pop()
	assert.True(t, r.CanGoBack(), "back to two entries")

	r.Pop()
	assert.False(t, r.CanGoBack(), "back to one entry")
}

func TestRouter_Depth(t *testing.T) {
	r := NewRouter()

	assert.Equal(t, 0, r.Depth())

	for i := 1; i <= 5; i++ {
		r.Push(mockView{title: "V"}, Scope{})
		assert.Equal(t, i, r.Depth())
	}

	r.Pop()
	assert.Equal(t, 4, r.Depth())

	r.PopToDepth(2)
	assert.Equal(t, 2, r.Depth())
}

func TestRouter_PushPreservesEarlierEntries(t *testing.T) {
	r := NewRouter()

	r.Push(mockView{title: "First"}, Scope{AccountID: "a"})
	r.Push(mockView{title: "Second"}, Scope{AccountID: "b"})

	// Verify the first entry is still accessible via breadcrumbs
	crumbs := r.Breadcrumbs()
	assert.Equal(t, "First", crumbs[0])
	assert.Equal(t, "Second", crumbs[1])

	// Pop back and verify the preserved entry
	r.Pop()
	assert.Equal(t, "First", r.Current().(mockView).title)
	assert.Equal(t, "a", r.CurrentScope().AccountID)
}
