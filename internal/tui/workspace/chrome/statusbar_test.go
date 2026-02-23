package chrome

import (
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/lipgloss"
	"github.com/stretchr/testify/assert"

	"github.com/basecamp/basecamp-cli/internal/tui"
)

func testStatusBar(width int) StatusBar {
	s := NewStatusBar(tui.NewStyles())
	s.SetWidth(width)
	return s
}

func helpBinding() key.Binding {
	return key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "help"))
}

func paletteBinding() key.Binding {
	return key.NewBinding(key.WithKeys("ctrl+p"), key.WithHelp("ctrl+p", "cmds"))
}

func TestStatusBar_GlobalHintsRendered(t *testing.T) {
	s := testStatusBar(80)
	s.SetGlobalHints([]key.Binding{helpBinding(), paletteBinding()})

	view := s.View()
	assert.Contains(t, view, "help")
	assert.Contains(t, view, "cmds")
}

func TestStatusBar_StatusTakesPrecedenceOverGlobalHints(t *testing.T) {
	s := testStatusBar(80)
	s.SetGlobalHints([]key.Binding{helpBinding()})
	s.SetStatus("Saving...", false)

	view := s.View()
	assert.Contains(t, view, "Saving...")
	// Global hints should NOT appear when status is set
	assert.NotContains(t, stripAnsi(view), "help")
}

func TestStatusBar_GlobalHintsTakesPrecedenceOverAccountName(t *testing.T) {
	s := testStatusBar(80)
	s.SetGlobalHints([]key.Binding{helpBinding()})
	s.SetAccount("My Company")

	view := stripAnsi(s.View())
	assert.Contains(t, view, "help")
	// Account name should NOT appear when global hints are set
	assert.NotContains(t, view, "My Company")
}

func TestStatusBar_AccountNameFallbackWithNoGlobalHints(t *testing.T) {
	s := testStatusBar(80)
	s.SetAccount("My Company")

	view := s.View()
	assert.Contains(t, view, "My Company")
}

func TestStatusBar_GlobalHintsTruncateOnNarrowTerminal(t *testing.T) {
	// Set view hints that consume most of the width
	s := testStatusBar(40)
	s.SetKeyHints([]key.Binding{
		key.NewBinding(key.WithKeys("j/k"), key.WithHelp("j/k", "navigate")),
		key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "open")),
	})
	s.SetGlobalHints([]key.Binding{helpBinding(), paletteBinding()})

	_ = stripAnsi(s.View())
	// With 40 chars, left hints take ~25 chars + min gap of 2 = 27.
	// Budget for globals is ~13 chars. "? help" = 6 chars fits, "ctrl+p cmds" = 11 chars may not.
	// At minimum, the bar should not overflow.
	assert.LessOrEqual(t, lipgloss.Width(s.View()), 40)

	// When extremely narrow, globals may be fully dropped
	s.SetWidth(20)
	_ = stripAnsi(s.View()) // Should not panic
}

func TestStatusBar_EmptyGlobalHintsShowAccountName(t *testing.T) {
	s := testStatusBar(80)
	s.SetGlobalHints(nil)
	s.SetAccount("Acme Corp")

	view := s.View()
	assert.Contains(t, view, "Acme Corp")
}

func TestStatusBar_DisabledGlobalHintsSkipped(t *testing.T) {
	s := testStatusBar(80)

	disabled := key.NewBinding(key.WithKeys("x"), key.WithHelp("x", "nope"))
	disabled.SetEnabled(false)
	s.SetGlobalHints([]key.Binding{disabled, helpBinding()})

	view := stripAnsi(s.View())
	assert.NotContains(t, view, "nope")
	assert.Contains(t, view, "help")
}

func TestStatusBar_SetStatus_IncrementsGen(t *testing.T) {
	s := testStatusBar(80)
	assert.Equal(t, uint64(0), s.StatusGen())

	s.SetStatus("Completed", false)
	assert.Equal(t, uint64(1), s.StatusGen())

	s.SetStatus("Trashed", false)
	assert.Equal(t, uint64(2), s.StatusGen())
}

func TestStatusBar_ClearStatus_Resets(t *testing.T) {
	s := testStatusBar(80)
	s.SetStatus("Saved!", false)
	assert.Contains(t, s.View(), "Saved!")

	s.ClearStatus()
	assert.NotContains(t, stripAnsi(s.View()), "Saved!")
}

// stripAnsi removes ANSI escape sequences for content assertions.
func stripAnsi(s string) string {
	var result strings.Builder
	inEscape := false
	for _, r := range s {
		if r == '\x1b' {
			inEscape = true
			continue
		}
		if inEscape {
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
				inEscape = false
			}
			continue
		}
		result.WriteRune(r)
	}
	return result.String()
}
