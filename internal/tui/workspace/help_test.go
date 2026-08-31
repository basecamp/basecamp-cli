package workspace

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
)

func TestModifiersLast(t *testing.T) {
	ordered := modifiersLast([]helpBinding{
		{"ctrl+r", "refresh"},
		{"enter", "open"},
		{"ctrl+c ctrl+c", "quit"},
		{"esc", "back"},
	})

	assert.Equal(t, []helpBinding{
		{"enter", "open"},
		{"esc", "back"},
		{"ctrl+r", "refresh"},
		{"ctrl+c ctrl+c", "quit"},
	}, ordered)
}

func TestHelpBar(t *testing.T) {
	bar := newHelpBar(plainStyles(t))
	bar.setWidth(80)
	bar.setBindings([]helpBinding{{"enter", "open"}, {"ctrl+r", "refresh"}})

	assert.Equal(t, "enter open • ctrl+r refresh", ansi.Strip(bar.view()))
	assert.Equal(t, 1, bar.height())
}

func TestHelpBarIsEmptyWithoutBindings(t *testing.T) {
	bar := newHelpBar(plainStyles(t))

	assert.Equal(t, "", bar.view())
	assert.Equal(t, 0, bar.height())
}

func TestHiddenHelpBarTakesNoRows(t *testing.T) {
	bar := newHelpBar(plainStyles(t))
	bar.setWidth(80)
	bar.setBindings([]helpBinding{{"enter", "open"}})
	bar.setHidden(true)

	assert.Equal(t, "", bar.view())
	assert.Equal(t, 0, bar.height())
}

func TestHelpBarWrapsToTheWidth(t *testing.T) {
	bar := newHelpBar(plainStyles(t))
	bar.setWidth(30)
	bar.setBindings([]helpBinding{
		{"enter", "open"},
		{"space", "complete"},
		{"ctrl+r", "refresh"},
	})

	lines := strings.Split(ansi.Strip(bar.view()), "\n")
	assert.Equal(t, 2, len(lines))
	assert.Equal(t, 2, bar.height())
	for _, line := range lines {
		assert.LessOrEqual(t, lipgloss.Width(line), 30)
	}
}

// A notice replaces the bar rather than joining it: it is the one thing the
// reader needs to see, and a row of shortcuts is not the place to hide it.
func TestHelpBarNotice(t *testing.T) {
	bar := newHelpBar(plainStyles(t))
	bar.setWidth(80)
	bar.setBindings([]helpBinding{{"enter", "open"}})
	bar.setNotice("Could not save the help preference")

	assert.Equal(t, "Could not save the help preference", ansi.Strip(bar.view()))

	bar.setNotice("")
	assert.Equal(t, "enter open", ansi.Strip(bar.view()))
}
