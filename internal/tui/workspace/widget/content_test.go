package widget

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/basecamp/basecamp-cli/internal/tui"
)

func TestContent_TrailingNewline_Scroll(t *testing.T) {
	styles := tui.NewStyles()
	c := NewContent(styles)
	c.SetSize(60, 5)
	c.SetContent("<p>Line one</p><p>Line two</p><p>Line three</p>")

	// Scroll to the bottom
	c.ScrollDown(100)

	view := c.View()
	lines := strings.Split(view, "\n")

	// The last visible line should contain actual content, not be empty
	last := lines[len(lines)-1]
	assert.NotEmpty(t, strings.TrimSpace(last), "last visible line at scroll bottom should contain content")
}
