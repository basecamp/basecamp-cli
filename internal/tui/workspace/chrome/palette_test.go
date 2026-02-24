package chrome

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/tui"
)

func splitLines(s string) []string {
	return strings.Split(s, "\n")
}

func testPalette() Palette {
	styles := tui.NewStyles()
	p := NewPalette(styles)
	p.SetSize(80, 24)
	p.SetActions(
		[]string{"Search", "New Message"},
		[]string{"Full-text search", "Compose a message"},
		[]string{"nav", "compose"},
		[]func() tea.Cmd{func() tea.Cmd { return nil }, func() tea.Cmd { return nil }},
	)
	return p
}

func TestPalette_NarrowWidth_NoNegative(t *testing.T) {
	p := testPalette()

	// SetSize with an extremely small width — must not panic.
	p.SetSize(2, 10)
	assert.GreaterOrEqual(t, p.input.Width, 0, "input.Width should never go negative")

	// Exercise View at narrow width.
	p.Focus()
	out := p.View()
	assert.NotEmpty(t, out)
}

func TestPalette_EscCloses(t *testing.T) {
	p := testPalette()
	p.Focus()

	cmd := p.Update(tea.KeyMsg{Type: tea.KeyEsc})
	require.NotNil(t, cmd)

	msg := cmd()
	_, ok := msg.(PaletteCloseMsg)
	assert.True(t, ok, "Esc should produce PaletteCloseMsg")
}

func TestPalette_EnterExecutesAction(t *testing.T) {
	var executed bool
	styles := tui.NewStyles()
	p := NewPalette(styles)
	p.SetSize(80, 24)
	p.SetActions(
		[]string{"Test Action"},
		[]string{"A test action"},
		[]string{"test"},
		[]func() tea.Cmd{func() tea.Cmd { executed = true; return nil }},
	)
	p.Focus()

	cmd := p.Update(tea.KeyMsg{Type: tea.KeyEnter})
	require.NotNil(t, cmd, "Enter should produce a batch cmd")

	// Execute the batch — it contains PaletteCloseMsg + PaletteExecMsg
	msg := cmd()
	_ = msg // batch msg
	assert.True(t, executed, "Enter should execute the selected action")
}

func TestPalette_FilterNarrows(t *testing.T) {
	p := testPalette()
	p.Focus()

	require.Len(t, p.filtered, 2, "all actions visible initially")

	// Type "Search" to filter
	p.input.SetValue("Search")
	p.refilter()

	assert.Len(t, p.filtered, 1, "filter should narrow to matching action")
	assert.Equal(t, "Search", p.filtered[0].Name)
}

func TestPalette_NarrowTerminal_NoOverflow(t *testing.T) {
	p := testPalette()
	p.SetSize(20, 10)
	p.Focus()

	view := p.View()
	lines := splitLines(view)
	for i, line := range lines {
		w := lipgloss.Width(line)
		assert.LessOrEqual(t, w, 20, "palette line %d overflows: %d > 20", i, w)
	}
}

func TestPalette_CursorScroll(t *testing.T) {
	// Build a palette with more actions than maxVisibleItems.
	n := maxVisibleItems + 5
	names := make([]string, n)
	descs := make([]string, n)
	cats := make([]string, n)
	execs := make([]func() tea.Cmd, n)
	for i := range n {
		names[i] = "Action" + string(rune('A'+i))
		descs[i] = "Desc"
		cats[i] = "cat"
		execs[i] = func() tea.Cmd { return nil }
	}

	p := NewPalette(tui.NewStyles())
	p.SetSize(80, 30)
	p.SetActions(names, descs, cats, execs)
	p.Focus()

	// Move cursor past maxVisibleItems
	for range maxVisibleItems + 2 {
		p.handleKey(tea.KeyMsg{Type: tea.KeyDown})
	}
	assert.Greater(t, p.cursor, maxVisibleItems-1, "cursor should be past visible window")

	// View should still render without panic and contain the focused action
	view := p.View()
	assert.Contains(t, view, names[p.cursor], "scrolled palette should show focused action")
}
