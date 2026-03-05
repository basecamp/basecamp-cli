package chrome

import (
	"testing"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"github.com/stretchr/testify/assert"

	"github.com/basecamp/basecamp-cli/internal/tui"
)

func testHelp(width, height int) Help {
	h := NewHelp(tui.NewStyles())
	h.SetSize(width, height)
	return h
}

func manyBindings(n int) [][]key.Binding {
	var rows [][]key.Binding
	for i := 0; i < n; i++ {
		rows = append(rows, []key.Binding{
			key.NewBinding(key.WithKeys("x"), key.WithHelp("x", "action")),
		})
	}
	return rows
}

func TestHelp_ScrollDown_AdvancesOffset(t *testing.T) {
	h := testHelp(80, 10)
	h.SetGlobalKeys(manyBindings(20))

	assert.Equal(t, 0, h.offset)

	h.Update(tea.KeyPressMsg{Code: 'j', Text: "j"})
	assert.Equal(t, 1, h.offset)

	h.Update(tea.KeyPressMsg{Code: 'j', Text: "j"})
	assert.Equal(t, 2, h.offset)
}

func TestHelp_ScrollUp_ClampsAtZero(t *testing.T) {
	h := testHelp(80, 10)
	h.SetGlobalKeys(manyBindings(20))

	// Already at top — should stay at 0
	h.Update(tea.KeyPressMsg{Code: 'k', Text: "k"})
	assert.Equal(t, 0, h.offset)

	// Scroll down then back up past zero
	h.Update(tea.KeyPressMsg{Code: 'j', Text: "j"})
	h.Update(tea.KeyPressMsg{Code: 'j', Text: "j"})
	h.Update(tea.KeyPressMsg{Code: 'k', Text: "k"})
	h.Update(tea.KeyPressMsg{Code: 'k', Text: "k"})
	h.Update(tea.KeyPressMsg{Code: 'k', Text: "k"})
	assert.Equal(t, 0, h.offset)
}

func TestHelp_Esc_SignalsClose(t *testing.T) {
	h := testHelp(80, 30)
	h.SetGlobalKeys(manyBindings(5))

	shouldClose, _ := h.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	assert.True(t, shouldClose)
}

func TestHelp_Q_SignalsClose(t *testing.T) {
	h := testHelp(80, 30)
	h.SetGlobalKeys(manyBindings(5))

	shouldClose, _ := h.Update(tea.KeyPressMsg{Code: 'q', Text: "q"})
	assert.True(t, shouldClose)
}

func TestHelp_QuestionMark_SignalsClose(t *testing.T) {
	h := testHelp(80, 30)
	h.SetGlobalKeys(manyBindings(5))

	shouldClose, _ := h.Update(tea.KeyPressMsg{Code: '?', Text: "?"})
	assert.True(t, shouldClose)
}

func TestHelp_OtherKey_NoClose(t *testing.T) {
	h := testHelp(80, 30)
	h.SetGlobalKeys(manyBindings(5))

	shouldClose, _ := h.Update(tea.KeyPressMsg{Code: 'x', Text: "x"})
	assert.False(t, shouldClose)
}

func TestHelp_HalfPageScroll(t *testing.T) {
	h := testHelp(80, 14) // visibleHeight = 14 - 4 = 10, half = 5
	h.SetGlobalKeys(manyBindings(30))

	h.Update(tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl})
	assert.Equal(t, 5, h.offset)

	h.Update(tea.KeyPressMsg{Code: 'u', Mod: tea.ModCtrl})
	assert.Equal(t, 0, h.offset)
}

func TestHelp_ResetScroll(t *testing.T) {
	h := testHelp(80, 10)
	h.SetGlobalKeys(manyBindings(20))

	h.Update(tea.KeyPressMsg{Code: 'j', Text: "j"})
	h.Update(tea.KeyPressMsg{Code: 'j', Text: "j"})
	assert.Greater(t, h.offset, 0)

	h.ResetScroll()
	assert.Equal(t, 0, h.offset)
}

func TestHelp_ScrollClampsToMax(t *testing.T) {
	h := testHelp(80, 10)
	h.SetGlobalKeys(manyBindings(3)) // small content

	// Scroll down many times — should clamp, not go negative or past content
	for i := 0; i < 50; i++ {
		h.Update(tea.KeyPressMsg{Code: 'j', Text: "j"})
	}
	assert.GreaterOrEqual(t, h.offset, 0)

	// Content fits — offset should be clamped to 0 since no overflow
	totalContent := h.contentLineCount()
	visible := h.visibleHeight()
	if totalContent <= visible {
		assert.Equal(t, 0, h.offset)
	}
}

func TestHelp_NoOverflow_ShowsEscClose(t *testing.T) {
	h := testHelp(80, 50) // plenty of room
	h.SetGlobalKeys(manyBindings(3))

	view := h.View()
	assert.Contains(t, view, "esc close")
	assert.NotContains(t, view, "j/k scroll")
}

func TestHelp_Overflow_ShowsScrollHint(t *testing.T) {
	h := testHelp(80, 10) // small viewport
	h.SetGlobalKeys(manyBindings(20))

	view := h.View()
	assert.Contains(t, view, "j/k scroll")
	assert.Contains(t, view, "esc close")
}
