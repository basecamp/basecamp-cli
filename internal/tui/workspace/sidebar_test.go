package workspace

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/tui"
)

// resize lays the model out for a terminal of the given size.
func resize(t *testing.T, m model, width, height int) model {
	t.Helper()

	updated, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: height})
	return updated.(model)
}

// The frame is two columns, and what it leaves is what the screen is told about.
func TestSidebarSplitsTheFrame(t *testing.T) {
	m := resize(t, newTestModel(t), 84, 20)

	assert.Equal(t, 28, m.sidebarWidth())
	assert.Equal(t, 84-sidebarGutter-28, m.contentWidth())

	home := m.nav.current().(*blank)
	assert.Equal(t, m.contentWidth(), home.width)
	assert.Less(t, home.width, 84, "the screen was handed the terminal, not its column")
}

// Every row of the frame is the terminal's width, sidebar or no sidebar: a row
// one column over wraps and shoves the whole screen down.
func TestSidebarKeepsEveryRowTheTerminalWidth(t *testing.T) {
	for _, width := range []int{40, 60, 84, 120, 200} {
		m := resize(t, newTestModel(t), width, 20)
		for _, line := range strings.Split(screen(m), "\n") {
			assert.LessOrEqual(t, lipgloss.Width(line), width, "at terminal width %d", width)
		}
	}
}

func TestSidebarTogglesOnShiftS(t *testing.T) {
	m := resize(t, newTestModel(t), 84, 20)
	require.Greater(t, m.sidebarWidth(), 0)
	require.Contains(t, screen(m), "│")

	m, _ = press(t, m, sidebarKey)
	assert.True(t, m.sidebar.hidden)
	assert.Equal(t, 0, m.sidebarWidth())
	assert.Equal(t, 84, m.contentWidth())
	assert.NotContains(t, screen(m), "│")

	m, _ = press(t, m, sidebarKey)
	assert.False(t, m.sidebar.hidden)
	assert.Greater(t, m.sidebarWidth(), 0)
}

// Hiding the sidebar hands the screen the columns it gave up, so a screen laid
// out for one width is told about the other.
func TestHidingTheSidebarResizesTheScreen(t *testing.T) {
	m := resize(t, newTestModel(t), 84, 20)
	home := m.nav.current().(*blank)
	require.Equal(t, m.contentWidth(), home.width)

	m, _ = press(t, m, sidebarKey)
	assert.Equal(t, 84, home.width)
}

// A terminal too narrow for both columns keeps the content and drops the
// sidebar, rather than squeezing the content past reading.
func TestSidebarStandsDownOnANarrowTerminal(t *testing.T) {
	narrow := resize(t, newTestModel(t), 50, 20)
	assert.Equal(t, 0, narrow.sidebarWidth())
	assert.False(t, narrow.sidebarAvailable())
	assert.Equal(t, 50, narrow.contentWidth())

	wide := resize(t, newTestModel(t), 84, 20)
	assert.True(t, wide.sidebarAvailable())
}

// It takes a third of the terminal, between a floor and a ceiling.
func TestSidebarWidthHasAFloorAndACeiling(t *testing.T) {
	assert.Equal(t, sidebarMinWidth, sidebarColumns(60))
	assert.Equal(t, 30, sidebarColumns(90))
	assert.Equal(t, sidebarMaxWidth, sidebarColumns(300))
}

// --- The divider ---

// The key that hides the sidebar is on the divider, so it can be found again
// once it is gone.
func TestDividerCarriesTheSidebarHint(t *testing.T) {
	m := resize(t, newTestModel(t), 84, 20)

	divider := strings.Split(screen(m), "\n")[2]
	assert.True(t, strings.HasSuffix(divider, " "+sidebarHintText+" ──"))

	m, _ = press(t, m, sidebarKey)
	divider = strings.Split(screen(m), "\n")[2]
	assert.Contains(t, divider, sidebarHintText, "the hint went with the sidebar it explains")
}

// A screen that wants the whole terminal has no sidebar, so the divider says
// nothing about one.
func TestDividerSaysNothingOnAFullWidthScreen(t *testing.T) {
	m := resize(t, newTestModel(t), 84, 20)
	m.push(newAccountPicker(m.ctx, true))
	m.relayout()

	assert.Equal(t, 0, m.sidebarWidth())
	assert.Equal(t, 84, m.contentWidth())
	assert.NotContains(t, screen(m), sidebarHintText)
}

// withUnreads is a loaded sidebar holding count unread readings, the first of
// them a ping when asked for. The badge counts what is in the sidebar rather
// than carrying a number of its own, so a test has to fill it.
func withUnreads(count int, ping bool) sidebar {
	items := make([]reading, count)
	for index := range items {
		items[index] = reading{title: fmt.Sprintf("Notification %d", index+1), unread: 1}
	}
	if ping && count > 0 {
		items[0].ping = true
	}
	return sidebar{loaded: true, readings: readings{unreads: items}}
}

func TestDividerCarriesTheBadge(t *testing.T) {
	m := resize(t, newTestModel(t), 84, 20)
	assert.NotContains(t, screen(m), "new")

	m.sidebar = withUnreads(4, false)
	divider := strings.Split(screen(m), "\n")[2]
	assert.Contains(t, divider, "4 new")
	assert.NotContains(t, divider, "ping")

	m.sidebar = withUnreads(4, true)
	divider = strings.Split(screen(m), "\n")[2]
	assert.Contains(t, divider, "4 new + ping")

	// The badge comes before the key hint.
	assert.Less(t, strings.Index(divider, "4 new"), strings.Index(divider, sidebarHintText))
}

// A divider too narrow for both gives up the key hint: the reader has seen it
// before, and the count is news.
func TestDividerGivesUpTheHintBeforeTheBadge(t *testing.T) {
	styles := plainStyles(t)
	badge := withUnreads(4, true).badge(styles)

	tight := ansi.Strip(renderDivider(30, styles, badge, sidebarHintText))
	assert.Equal(t, 30, lipgloss.Width(tight))
	assert.Contains(t, tight, "4 new + ping")
	assert.NotContains(t, tight, sidebarHintText)
}

// Whatever it gives up, the divider is exactly the width it was given.
func TestDividerAlwaysFillsTheWidth(t *testing.T) {
	styles := plainStyles(t)
	badge := withUnreads(12, true).badge(styles)

	for width := 1; width <= 120; width++ {
		divider := ansi.Strip(renderDivider(width, styles, badge, sidebarHintText))
		assert.Equal(t, width, lipgloss.Width(divider), "at width %d", width)
	}
}

// --- The badge ---

func TestBadgeIsSilentWithNothingNew(t *testing.T) {
	styles := plainStyles(t)

	assert.Equal(t, "", sidebar{}.badge(styles))
	assert.Equal(t, "", withUnreads(0, true).badge(styles))
}

func TestBadgeText(t *testing.T) {
	styles := plainStyles(t)

	assert.Equal(t, "1 new", ansi.Strip(withUnreads(1, false).badge(styles)))
	assert.Equal(t, "4 new", ansi.Strip(withUnreads(4, false).badge(styles)))
	assert.Equal(t, "3 new + ping", ansi.Strip(withUnreads(3, true).badge(styles)))
}

// The count takes the accent and the ping takes the error color, which on an
// Omarchy palette is the theme's own red. Both come from the theme, so a retint
// carries them.
func TestBadgeTakesItsColorsFromTheTheme(t *testing.T) {
	theme := tui.DefaultTheme(true)
	badge := withUnreads(3, true).badge(tui.NewStylesWithTheme(theme))

	assert.Contains(t, badge,
		lipgloss.NewStyle().Foreground(theme.Primary).Bold(true).Render("3 new"))
	assert.Contains(t, badge,
		lipgloss.NewStyle().Foreground(theme.Error).Bold(true).Render("ping"))
}

// NO_COLOR takes the color out and leaves the words, like everything else here.
func TestBadgeWithoutColor(t *testing.T) {
	badge := withUnreads(3, true).badge(plainStyles(t))

	assert.NotContains(t, badge, "\x1b[38")
	assert.Contains(t, ansi.Strip(badge), "3 new + ping")
}
